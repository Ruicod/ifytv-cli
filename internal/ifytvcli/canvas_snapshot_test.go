package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const snapshotTestPath = "/api/v1/cli/projects/" + projectID + "/snapshot"

func snapshotTestOptions() options {
	return options{command: "canvas inspect", project: projectID, authorizationID: productionTestConsent}
}

func snapshotTestPage(revision uint64, canEdit bool, groups, nodes, connections, cursor string) string {
	return fmt.Sprintf(`{"project_id":%q,"title":"镜头画布","revision":%d,"can_edit":%t,"groups":%s,"nodes":%s,"connections":%s,"next_cursor":%q}`,
		projectID, revision, canEdit, groups, nodes, connections, cursor)
}

func snapshotTestJSON(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode canvas JSON: %v", err)
	}
	return value
}

func snapshotTestRender(t *testing.T, result output) map[string]json.RawMessage {
	t.Helper()
	var buf bytes.Buffer
	if err := render(&buf, &buf, result, true); err != nil {
		t.Fatal(err)
	}
	return snapshotTestJSON(t, buf.Bytes())
}

func TestCanvasInspectAssemblesReadOnlySnapshot(t *testing.T) {
	const cursor = "page+two /?&=#"
	const revision = 9007199254740993
	const untrustedURL = "https://untrusted.invalid/private"
	first := snapshotTestPage(revision, false,
		`[{"id":"_child","title":"分镜","parent_id":"root"}]`,
		fmt.Sprintf(`[{"id":"_n-1","kind":"image","title":"第一镜","prompt":"山雨","group_id":"_child","selection":"selected","assets":[{"id":%q,"kind":"image","role":"output","mime_type":"image/png","selection":"selected","name":"分镜图","url":%q}],"generation":{"status":"succeeded","task_id":%q,"progress":100}}]`, assetID, untrustedURL, taskID),
		`[{"id":"-edge","source_node_id":"_n-1","target_node_id":"n-2","source_port":"out","target_port":"ref"}]`,
		cursor)
	second := snapshotTestPage(revision, false, `[{"id":"root","title":"剧集"}]`, `[{"id":"n-2","kind":"text","text":"保留文字","group_id":"root","selection":"candidate"}]`, "[]", "")
	var requests atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != snapshotTestPath ||
			r.Header.Get("Authorization") != "Bearer "+deviceToken ||
			r.Header.Get("Idempotency-Key") != "" || r.ContentLength != 0 {
			t.Errorf("unexpected canvas request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch requests.Add(1) {
		case 1:
			if r.URL.RawQuery != "" {
				t.Error("first page carried a cursor")
			}
			jsonReply(w, http.StatusOK, first)
		case 2:
			if r.URL.RawQuery != "cursor="+url.QueryEscape(cursor) {
				t.Errorf("opaque cursor changed: %q", r.URL.RawQuery)
			}
			jsonReply(w, http.StatusOK, second)
		default:
			t.Error("extra snapshot request")
			http.NotFound(w, r)
		}
	})

	result, problem := c.execute(context.Background(), snapshotTestOptions())
	if problem != nil || requests.Load() != 2 {
		t.Fatalf("read-only snapshot: %v, requests=%d", problem, requests.Load())
	}
	if result.ProjectURL != c.base.String()+"/canvas/cloud/"+projectID {
		t.Fatalf("wrong project link: %q", result.ProjectURL)
	}
	canvas := snapshotTestRender(t, result)["canvas"]
	value := snapshotTestJSON(t, canvas)
	if bytes.Contains(canvas, []byte(untrustedURL)) || value["next_cursor"] != nil {
		t.Fatal("snapshot published a server URL or its pagination cursor")
	}
	if string(value["title"]) != `"镜头画布"` || string(value["revision"]) != "9007199254740993" ||
		string(value["can_edit"]) != "false" || string(value["project_id"]) != fmt.Sprintf("%q", projectID) {
		t.Fatalf("unexpected project metadata: %s", canvas)
	}
	for key, want := range map[string]int{"groups": 2, "nodes": 2, "connections": 1} {
		var items []json.RawMessage
		if err := json.Unmarshal(value[key], &items); err != nil || len(items) != want {
			t.Fatalf("%s: got %s, want %d entries", key, value[key], want)
		}
	}
	for _, want := range []string{`"text":"保留文字"`, `"prompt":"山雨"`, `"task_id":"` + taskID + `"`, `"id":"` + assetID + `"`, `"role":"output"`, `"mime_type":"image/png"`, `"selection":"selected"`, `"id":"-edge"`} {
		if !bytes.Contains(canvas, []byte(want)) {
			t.Errorf("snapshot did not preserve %s", want)
		}
	}
}

func TestCanvasInspectWritesCompleteSnapshotWithoutOverwriting(t *testing.T) {
	o := snapshotTestOptions()
	o.out = filepath.Join(t.TempDir(), "snapshot.json")
	var requests atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != snapshotTestPath {
			http.NotFound(w, r)
			return
		}
		jsonReply(w, http.StatusOK, snapshotTestPage(42, true, "[]", "[]", "[]", ""))
	})
	result, problem := c.execute(context.Background(), o)
	if problem != nil {
		t.Fatalf("write snapshot: %v", problem)
	}
	// The state-file checker also verifies the final file stayed private after publication.
	private, err := openDeviceRecord(o.out)
	if err != nil {
		t.Fatalf("snapshot permissions: %v", err)
	}
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(o.out)
	if err != nil {
		t.Fatal(err)
	}
	value := snapshotTestJSON(t, data)
	if result.Path != o.out || result.Bytes != int64(len(data)) ||
		snapshotTestRender(t, result)["canvas"] != nil || string(value["revision"]) != "42" ||
		string(value["nodes"]) != "[]" || value["next_cursor"] != nil {
		t.Fatalf("unexpected file result: path=%q bytes=%d file=%s", result.Path, result.Bytes, data)
	}
	_, second := c.execute(context.Background(), o)
	again, err := os.ReadFile(o.out)
	if second == nil || second.Code != "output_exists" || requests.Load() != 1 || err != nil || !bytes.Equal(data, again) {
		t.Fatalf("repeat export: %v, requests=%d, read error=%v", second, requests.Load(), err)
	}
}

func TestCanvasInspectRejectsIncoherentSnapshotBeforePublishing(t *testing.T) {
	const firstNode = `[{"id":"node-a","kind":"text"}]`
	for _, test := range []struct {
		name, second string
	}{
		{"changed revision", snapshotTestPage(43, true, "[]", "[]", "[]", "")},
		{"changed permission", snapshotTestPage(42, false, "[]", "[]", "[]", "")},
		{"changed title", strings.Replace(snapshotTestPage(42, true, "[]", "[]", "[]", ""), "镜头画布", "新标题", 1)},
		{"wrong project", strings.Replace(snapshotTestPage(42, true, "[]", "[]", "[]", ""), projectID, taskID, 1)},
		{"duplicate node", snapshotTestPage(42, true, "[]", firstNode, "[]", "")},
		{"repeated cursor", snapshotTestPage(42, true, "[]", "[]", "[]", "more")},
		{"dangling edge", snapshotTestPage(42, true, "[]", "[]", `[{"id":"edge","source_node_id":"node-a","target_node_id":"missing"}]`, "")},
		{"dangling group", snapshotTestPage(42, true, `[{"id":"g","title":"Group","parent_id":"missing"}]`, "[]", "[]", "")},
		{"missing arrays", snapshotTestPage(42, true, "[]", "null", "[]", "")},
		{"missing can_edit", strings.Replace(snapshotTestPage(42, true, "[]", "[]", "[]", ""), `"can_edit":true,`, "", 1)},
		{"credential in card text", snapshotTestPage(42, true, "[]", fmt.Sprintf(`[{"id":"node-b","kind":"text","text":%q}]`, "Bearer "+deviceToken), "[]", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := snapshotTestOptions()
			o.out = filepath.Join(t.TempDir(), "snapshot.json")
			var requests atomic.Int32
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != snapshotTestPath {
					http.NotFound(w, r)
					return
				}
				switch requests.Add(1) {
				case 1:
					jsonReply(w, http.StatusOK, snapshotTestPage(42, true, "[]", firstNode, "[]", "more"))
				case 2:
					jsonReply(w, http.StatusOK, test.second)
				default:
					t.Error("read past invalid page")
					http.NotFound(w, r)
				}
			})
			result, problem := c.execute(context.Background(), o)
			if problem == nil || problem.Code != "invalid_response" || result.Path != "" ||
				snapshotTestRender(t, result)["canvas"] != nil || requests.Load() != 2 {
				t.Fatalf("invalid snapshot was not rejected: %v, path=%q, requests=%d", problem, result.Path, requests.Load())
			}
			entries, err := os.ReadDir(filepath.Dir(o.out))
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial snapshot output: %v %v", entries, err)
			}
		})
	}
}

func TestCanvasInspectPreservesGrantAndAuthenticationFailures(t *testing.T) {
	for _, test := range []struct {
		name, code, want string
		status           int
		grant            bool
	}{
		{"missing endpoint", "", "canvas_read_unavailable", http.StatusNotFound, false},
		{"device scope denied", "device_scope_denied", "canvas_read_unavailable", http.StatusForbidden, false},
		{"missing project", "canvas_not_found", "canvas_not_found", http.StatusNotFound, false},
		{"grant required", "cli_grant_required", "cli_grant_required", http.StatusForbidden, true},
		{"revoked device", "device_unauthorized", "device_unauthorized", http.StatusUnauthorized, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != snapshotTestPath {
					t.Errorf("unexpected canvas path %q", r.URL.Path)
				}
				jsonReply(w, test.status, fmt.Sprintf(`{"error":{"code":%q},"authorization_url":"https://untrusted.invalid/"}`, test.code))
			})
			result, problem := c.execute(context.Background(), snapshotTestOptions())
			if problem == nil || problem.Code != test.want || problem.HTTPStatus != test.status ||
				problem.invalidDevice != (test.code == "device_unauthorized") {
				t.Fatalf("expected %s, got %v", test.want, problem)
			}
			wantURL := ""
			if test.grant {
				wantURL = c.base.String() + "/cli?project=" + projectID
			}
			if result.AuthorizationURL != wantURL {
				t.Fatalf("wrong grant URL: %q", result.AuthorizationURL)
			}
		})
	}
}

func TestCanvasInspectRejectsForeignRedirect(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		http.NotFound(w, r)
	}))
	defer foreign.Close()
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+snapshotTestPath, http.StatusTemporaryRedirect)
	})
	_, problem := c.execute(context.Background(), snapshotTestOptions())
	if problem == nil || problem.Code != "redirect_rejected" || foreignCalls.Load() != 0 {
		t.Fatalf("foreign redirect result: %v, foreign requests=%d", problem, foreignCalls.Load())
	}
}

func TestCanvasInspectRequiresPinnedDeviceAuthorization(t *testing.T) {
	c := productionTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("requested canvas without pinned authorization")
	})
	o := snapshotTestOptions()
	o.authorizationID = ""
	_, problem := c.execute(context.Background(), o)
	if problem == nil || problem.Code != "device_login_required" {
		t.Fatalf("expected pinned authorization requirement, got %v", problem)
	}
}

func TestCanvasInspectStopsAtPageLimit(t *testing.T) {
	var requests atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		if count > 32 || r.URL.Path != snapshotTestPath {
			t.Error("read past the canvas page limit")
			http.NotFound(w, r)
			return
		}
		jsonReply(w, http.StatusOK, snapshotTestPage(42, false, "[]", "[]", "[]", fmt.Sprint(count)))
	})
	result, problem := c.execute(context.Background(), snapshotTestOptions())
	if problem == nil || problem.Code != "canvas_snapshot_too_large" ||
		requests.Load() != 32 || snapshotTestRender(t, result)["canvas"] != nil {
		t.Fatalf("page limit result: %v, requests=%d", problem, requests.Load())
	}
}
