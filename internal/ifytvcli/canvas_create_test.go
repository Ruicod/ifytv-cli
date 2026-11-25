package ifytvcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const canvasTeamID = "b5984a49-efe3-4d74-aa3d-a39264513d01"

func useCanvasDevice(t *testing.T, site string) string {
	t.Helper()
	t.Setenv("IFYTV_API_KEY", "")
	c, problem := newClient(site, "", true)
	if problem != nil {
		t.Fatal(problem)
	}
	s, problem := c.openSession(context.Background())
	if problem != nil {
		t.Fatal(problem)
	}
	defer s.file.unlock()
	login := digest([]byte("canvas test login"))
	s.RunScope = digest([]byte(site + "\x00" + testKey))
	s.Credential = &deviceCredential{Token: deviceToken, ExpiresAt: time.Now().Add(time.Hour), LoginID: login}
	if problem := s.save(); problem != nil {
		t.Fatal(problem)
	}
	return login
}

func teamList(w http.ResponseWriter) {
	jsonReply(w, 200, fmt.Sprintf(`{"workspaces":[{"id":%q,"name":"Studio","kind":"team","can_create_project":true}]}`, canvasTeamID))
}

func TestAutoCanvasResumesToOriginalCard(t *testing.T) {
	ctx := context.Background()
	const node = "first-video"
	dir := t.TempDir()
	localImage(t, dir, "frame.png", "picture")
	spec := newSpec()
	spec["images"] = []string{"frame.png"}
	spec["audio"] = []string{"https://audio.example/one.mp3", "https://audio.example/two.mp3"}
	file := writeSpec(t, dir, spec)
	var lists, creates, uploads, tasks atomic.Int32
	var firstKey, firstBody string
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" && r.Header.Get("Authorization") != "Bearer "+deviceToken {
			t.Error("missing bound device authorization", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/cli/workspaces":
			lists.Add(1)
			teamList(w)
		case "/api/v1/cli/projects":
			data, _ := io.ReadAll(r.Body)
			key := r.Header.Get("Idempotency-Key")
			if creates.Add(1) == 1 {
				firstBody, firstKey = string(data), key
				var body map[string]string
				if json.Unmarshal(data, &body) != nil || len(body) != 2 || body["title"] != "scene" || body["workspace_id"] != canvasTeamID || !validUUID(key) {
					t.Error("wrong project request", string(data), key)
				}
				// The server committed the project, but its first receipt was lost.
				http.Error(w, "lost receipt", http.StatusBadGateway)
				return
			}
			if string(data) != firstBody || firstKey != key {
				t.Error("changed project replay")
			}
			jsonReply(w, 200, fmt.Sprintf(`{"project_id":%q,"workspace_id":%q}`, projectID, canvasTeamID))
		case "/api/v1/uploads":
			if creates.Load() != 2 {
				t.Error("uploaded before a verified project receipt")
			}
			uploads.Add(1)
			uploadReply(w, "frame", "image")
		case "/api/v1/cli/projects/" + projectID + "/runs":
			if tasks.Add(1) != 1 || uploads.Load() != 1 || !validUUID(r.Header.Get("Idempotency-Key")) || r.Header.Get("Idempotency-Key") == firstKey {
				t.Error("duplicate submission, premature run, or reused project key")
			}
			var v struct {
				Spec  videoSpec `json:"spec"`
				Names []string  `json:"reference_names"`
			}
			if json.NewDecoder(r.Body).Decode(&v) != nil || v.Spec.Prompt != spec["prompt"] || !slices.Equal(v.Spec.Images, []string{"https://media.example/frame"}) || !slices.Equal(v.Spec.Audio, spec["audio"].([]string)) || !slices.Equal(v.Names, []string{"frame.png", "audio 1", "audio 2"}) {
				t.Error("lost ordered references, labels, or prompt", v)
			}
			jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"project_id":%q,"node_id":%q,"status":"queued"}`, taskID, projectID, node))
		case "/api/v1/tasks/" + taskID:
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded"}`, taskID))
		case "/api/v1/cli/projects/" + projectID + "/videos":
			jsonReply(w, 200, fmt.Sprintf(`{"project_id":%q,"node_id":%q,"asset_id":%q,"duplicate":true}`, projectID, node, assetID))
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	login := useCanvasDevice(t, site.URL)
	baseArgs := []string{"video", "create", "--run", "scene", "--spec", file, "--authorization-id", login, "--json"}
	first := invoke(ctx, append(slices.Clone(baseArgs), "--project", "auto")...).result(t, false)
	if first["workspace_id"] != canvasTeamID || creates.Load() != 1 || tasks.Load() != 0 || uploads.Load() != 0 {
		t.Fatal(first)
	}
	expectError(t, invoke(ctx, append(slices.Clone(baseArgs), "--project", "auto", "--workspace", assetID)...).result(t, false), "run_project_conflict")
	modified := newSpec()
	modified["prompt"] = "a different film"
	replaced := writeSpec(t, t.TempDir(), modified)
	expectError(t, invoke(ctx, "video", "create", "--run", "scene", "--spec", replaced, "--json").result(t, false), "run_conflict")
	recovery := invoke(ctx, baseArgs...)
	got := recovery.result(t, true)
	link := site.URL + "/canvas/cloud/" + projectID
	if got["project_url"] != link+"?node="+node || got["task_id"] != taskID || !strings.Contains(recovery.diagnostics, link) || strings.Index(recovery.diagnostics, link) > strings.Index(recovery.diagnostics, "Uploaded") {
		t.Fatal(got, recovery.diagnostics)
	}
	invoke(ctx, append(slices.Clone(baseArgs), "--project", link+"?node=other")...).result(t, true)
	wait := invoke(ctx, "video", "wait", "--run", "scene", "--authorization-id", login, "--json").result(t, true)
	if wait["workspace_id"] != canvasTeamID || wait["project_url"] != got["project_url"] || wait["node_id"] != node || wait["asset_id"] != assetID || lists.Load() != 1 || creates.Load() != 2 || tasks.Load() != 1 {
		t.Fatal(wait)
	}
}

func TestStandaloneCanvasReuseAndExistingLinks(t *testing.T) {
	ctx := context.Background()
	var creates, lists, runs atomic.Int32
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli/workspaces":
			lists.Add(1)
			teamList(w)
		case "/api/v1/cli/projects":
			creates.Add(1)
			jsonReply(w, 201, fmt.Sprintf(`{"workspace_id":%q,"project_id":%q}`, canvasTeamID, projectID))
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/cli/projects/" + projectID + "/runs":
			runs.Add(1)
			jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"status":"queued","project_id":%q,"node_id":"n"}`, taskID, projectID))
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	login := useCanvasDevice(t, site.URL)
	link := site.URL + "/canvas/cloud/" + projectID
	args := []string{"canvas", "create", "--run", "shared", "--authorization-id", login, "--json"}
	results := make(chan invocation, 2)
	for range 2 {
		go func() { results <- invoke(ctx, args...) }()
	}
	for range 2 {
		if got := (<-results).result(t, true); got["project_url"] != link || got["workspace_id"] != canvasTeamID {
			t.Fatal(got)
		}
	}
	pending := invoke(ctx, "video", "status", "--run", "shared", "--json").result(t, false)
	expectError(t, pending, "run_pending")
	if !strings.Contains(pending["error"].(map[string]any)["message"].(string), "canvas create") {
		t.Fatal("a standalone canvas recovery must not suggest a paid task", pending)
	}
	if pending["project_url"] != link || runs.Load() != 0 {
		t.Fatal(pending)
	}
	file := writeSpec(t, t.TempDir(), newSpec())
	for _, run := range []string{"shared", "explicit"} {
		got := invoke(ctx, "video", "create", "--run", run, "--spec", file, "--project", link+"?node=ignored&__app_version=v1#view", "--json").result(t, true)
		if got["project_url"] != link+"?node=n" {
			t.Fatal(got)
		}
	}
	if lists.Load() != 1 || creates.Load() != 1 || runs.Load() != 2 {
		t.Fatal(lists.Load(), creates.Load(), runs.Load())
	}
	expectError(t, invoke(ctx, "video", "create", "--run", "other", "--spec", file, "--project", "https://another.example/canvas/cloud/"+projectID, "--json").result(t, false), "project_site_mismatch")
}

func TestAutoCanvasKeepsIntentWhileChoosingTeam(t *testing.T) {
	ctx := context.Background()
	var mode, direct, projectCreates atomic.Int32
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/cli/workspaces":
			if mode.Load() == 0 {
				http.NotFound(w, r)
				return
			}
			jsonReply(w, 200, fmt.Sprintf(`{"workspaces":[{"name":"A","id":%q,"kind":"team","can_create_project":true},{"name":"B","id":%q,"kind":"team","can_create_project":true}]}`, canvasTeamID, assetID))
		case "/api/v1/cli/projects":
			projectCreates.Add(1)
			var request map[string]string
			if json.NewDecoder(r.Body).Decode(&request) != nil || request["workspace_id"] != assetID {
				t.Error("did not preserve explicitly chosen team", request)
			}
			jsonReply(w, 201, fmt.Sprintf(`{"workspace_id":%q,"project_id":%q}`, assetID, projectID))
		case "/api/v1/cli/projects/" + projectID + "/runs":
			jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"status":"queued","project_id":%q,"node_id":"n"}`, taskID, projectID))
		case "/api/v1/tasks":
			direct.Add(1)
			created(w)
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	login := useCanvasDevice(t, site.URL)
	file := writeSpec(t, t.TempDir(), newSpec())
	args := []string{"video", "create", "--run", "choose", "--spec", file, "--authorization-id", login, "--json"}
	execute := func(extra ...string) invocation { return invoke(ctx, append(slices.Clone(args), extra...)...) }
	expectError(t, execute("--project", "auto").result(t, false), "canvas_creation_unavailable")
	expectError(t, invoke(ctx, "video", "status", "--run", "choose", "--json").result(t, false), "run_pending")
	mode.Store(1)
	unresolved := execute().result(t, false)
	expectError(t, unresolved, "workspace_selection_required")
	if choices, ok := unresolved["workspaces"].([]any); !ok || len(choices) != 2 || projectCreates.Load() != 0 || direct.Load() != 0 {
		t.Fatal(unresolved)
	}
	mode.Store(0)
	expectError(t, execute("--project", "auto", "--workspace", assetID).result(t, false), "canvas_creation_unavailable")
	mode.Store(1)
	if got := execute().result(t, true); got["workspace_id"] != assetID || projectCreates.Load() != 1 || direct.Load() != 0 {
		t.Fatal(got)
	}
}

func TestCanvasTargetFlagsRejectAmbiguity(t *testing.T) {
	ctx := context.Background()
	file := writeSpec(t, t.TempDir(), newSpec())
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected call", r.URL.Path)
		http.NotFound(w, r)
	})
	expectError(t, invoke(ctx, "video", "create", "--spec", file, "--run", "one", "--project", projectID, "--project", "auto", "--json").result(t, false), "usage")
}

func TestCanvasTeamChoicePinsCurrentDeviceApproval(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli/workspaces" {
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		jsonReply(w, 200, fmt.Sprintf(`{"workspaces":[{"id":%q,"name":"Team 1","kind":"team","can_create_project":true},{"id":%q,"name":"Team 2","kind":"team","can_create_project":true}]}`, canvasTeamID, assetID))
	})
	login := useCanvasDevice(t, site.URL)
	list := invoke(ctx, "canvas", "workspaces", "--json").result(t, true)
	choice := invoke(ctx, "canvas", "create", "--run", "choice", "--json").result(t, false)
	expectError(t, choice, "workspace_selection_required")
	if list["authorization_id"] != login || choice["authorization_id"] != login {
		t.Fatal("cannot bind the selected team to its device approval", list, choice)
	}
	c, problem := newClient(site.URL, "", true)
	if problem != nil {
		t.Fatal(problem)
	}
	s, problem := c.openSession(ctx)
	if problem != nil {
		t.Fatal(problem)
	}
	s.Credential.LoginID = digest([]byte("different device approval"))
	s.RunScope = digest([]byte("another account"))
	problem = s.save()
	s.file.unlock()
	if problem != nil {
		t.Fatal(problem)
	}
	expectError(t, invoke(ctx, "canvas", "create", "--run", "choice", "--workspace", canvasTeamID, "--authorization-id", login, "--json").result(t, false), "authorization_changed")
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}

func TestCanvasRunRecordRequiresConfirmedProjectBeforeSubmission(t *testing.T) {
	valid := record{Version: 3, Stage: "prepared", WorkspaceID: canvasTeamID, ProjectID: projectID, ProjectKey: assetID, Key: taskID, SpecHash: digest([]byte("spec")), Payload: json.RawMessage(`{"spec":{"model":"test-h3"},"reference_names":[]}`)}
	if !valid.valid() {
		t.Fatal("expected a valid prepared canvas run")
	}
	for _, modify := range []func(*record){
		func(r *record) { r.ProjectID = "" }, func(r *record) { r.WorkspaceID = "" }, func(r *record) { r.ProjectKey = r.Key },
		func(r *record) { r.Version = 1 }, func(r *record) { r.Version = 2 }, func(r *record) { r.Payload = json.RawMessage(`{"model":"test-h3"}`) },
	} {
		r := valid
		modify(&r)
		if r.valid() {
			t.Fatal("accepted damaged destination state", r)
		}
	}
}
