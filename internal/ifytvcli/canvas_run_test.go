package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanvasRunPersistsDestinationAndRetriesOriginalRequest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	localImage(t, dir, "参考.png", "frame")
	spec := newSpec()
	spec["images"] = []string{"参考.png"}
	spec["audio"] = []string{"https://audio.example/signed?private=signature"}
	file := writeSpec(t, dir, spec)
	var posts atomic.Int32
	var historyRemoved atomic.Bool
	var quotaReached atomic.Bool
	var originalBody []byte
	var originalKey string
	const node = "48a741b6-101d-43bb-9052-1ad1d864e2ec"
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			uploadReply(w, "staged-image", "image")
		case "/api/v1/cli/projects/" + projectID + "/runs":
			body, err := io.ReadAll(r.Body)
			if err != nil || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Errorf("unexpected project request: %v", err)
			}
			if posts.Add(1) == 1 {
				originalBody, originalKey = body, r.Header.Get("Idempotency-Key")
				var envelope struct {
					Spec  videoSpec `json:"spec"`
					Names []string  `json:"reference_names"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil || !validUUID(originalKey) ||
					!slices.Equal(envelope.Names, []string{"参考.png", "audio 1"}) ||
					!slices.Equal(envelope.Spec.Images, []string{"https://media.example/staged-image"}) ||
					!slices.Equal(envelope.Spec.Audio, spec["audio"].([]string)) || envelope.Spec.Prompt != spec["prompt"] {
					t.Errorf("lost prompt, references or labels: %+v (%v)", envelope, err)
				}
				jsonReply(w, 403, `{"error":{"code":"forbidden"}}`)
				return
			}
			if r.Header.Get("Idempotency-Key") != originalKey || !bytes.Equal(body, originalBody) {
				t.Error("changed request during recovery")
			}
			if posts.Load() == 2 { // A lost or misdirected receipt cannot finalize the run.
				jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"status":"queued","project_id":%q,"node_id":%q}`, taskID, assetID, node))
				return
			}
			jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"status":"queued","project_id":%q,"node_id":%q,"project_url":"https://untrusted.example/"}`, taskID, projectID, node))
		case "/api/v1/tasks/" + taskID:
			if historyRemoved.Load() {
				jsonReply(w, http.StatusNotFound, `{"error":{"code":"task_not_found"}}`)
				return
			}
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded"}`, taskID))
		case "/api/v1/cli/projects/" + projectID + "/videos":
			if quotaReached.Load() {
				jsonReply(w, http.StatusTooManyRequests, `{"error":{"code":"canvas_asset_daily_limit"}}`)
				return
			}
			jsonReply(w, 200, fmt.Sprintf(`{"project_id":%q,"node_id":%q,"asset_id":%q,"duplicate":true}`, projectID, node, assetID))
		default:
			t.Errorf("unexpected request, possible extra paid create: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	create := func(project string) invocation {
		args := []string{"video", "create", "--run", "scene", "--spec", file, "--json"}
		if project != "" {
			args = append(args, "--project", project)
		}
		return invoke(ctx, args...)
	}
	if got := create(projectID).result(t, false); got["authorization_url"] != site.URL+"/cli?project="+projectID || posts.Load() != 1 {
		t.Fatalf("expected a scoped project authorization URL: %v", got)
	}
	if err := os.Remove(filepath.Join(dir, "参考.png")); err != nil {
		t.Fatal(err)
	}
	if got := create(assetID).result(t, false); got["error"].(map[string]any)["code"] != "run_project_conflict" {
		t.Fatal(got)
	}
	if got := create("").result(t, false); got["error"].(map[string]any)["code"] != "invalid_response" {
		t.Fatal(got)
	}
	for _, project := range []string{"", projectID} {
		got := create(project).result(t, true)
		if got["node_id"] != node || got["project_url"] != site.URL+"/canvas/cloud/"+projectID+"?node="+node || got["task_id"] != taskID {
			t.Fatal(got)
		}
	}
	status := invoke(ctx, "video", "status", "--run", "scene", "--json").result(t, true)
	wait := invoke(ctx, "video", "wait", "--run", "scene", "--json").result(t, true)
	if status["node_id"] != node || wait["node_id"] != node || wait["asset_id"] != assetID || posts.Load() != 3 {
		t.Fatal(status, wait, posts.Load())
	}
	historyRemoved.Store(true)
	saved := invoke(ctx, "video", "wait", "--run", "scene", "--json").result(t, true)
	if saved["node_id"] != node || saved["asset_id"] != assetID || saved["status"] != "succeeded" || posts.Load() != 3 {
		t.Fatal("wait did not recover the completed canvas receipt after task expiry", saved)
	}
	historyRemoved.Store(false)
	quotaReached.Store(true)
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	blocked := invoke(deadline, "video", "wait", "--run", "scene", "--json").result(t, false)
	if blocked["error"].(map[string]any)["code"] != "canvas_asset_daily_limit" || blocked["node_id"] != node {
		t.Fatal("a daily storage limit requires action on the original card", blocked)
	}
}

func TestCanvasRunNeverFallsBackToOffCanvasSubmission(t *testing.T) {
	ctx := context.Background()
	var direct, scoped atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			direct.Add(1)
			created(w)
		case "/api/v1/cli/projects/" + projectID + "/runs":
			scoped.Add(1)
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	file := writeSpec(t, t.TempDir(), newSpec())
	createRun(file, "old").result(t, true)
	for _, tc := range []struct{ run, project, code string }{
		{"old", projectID, "run_project_conflict"}, {"new", "invalid", "invalid_project"}, {"new", "", "invalid_project"}, {"new", projectID, "http_error"},
	} {
		got := invoke(ctx, "video", "create", "--run", tc.run, "--spec", file, "--project", tc.project, "--json").result(t, false)
		if got["error"].(map[string]any)["code"] != tc.code {
			t.Fatal(got)
		}
	}
	if direct.Load() != 1 || scoped.Load() != 1 {
		t.Fatalf("direct %d scoped %d", direct.Load(), scoped.Load())
	}
}
