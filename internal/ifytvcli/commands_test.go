package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testKey     = "test-private-value-403"
	taskID      = "56ffe246-d3b1-42f7-ad4e-9f3bc149f7d2"
	projectID   = "003d9550-c1bb-4892-84ff-307fb7a2e5b6"
	assetID     = "db554bb8-cc31-4279-8fad-5c0f1e816e99"
	catalogJSON = `{"models":[{"id":"test-h3","display_name":"Test H3","modes":["omni","text_to_video","image_to_video","first_last_frame"],"ratios":["16:9"],"resolutions":["768p"],"default_resolution":"768p","duration_kind":"fixed","fixed_duration":10,"min_images":0,"max_images":9,"video_supported":true,"max_videos":3,"video_modes":["omni"],"audio_supported":true,"max_audios":3,"audio_modes":["omni"],"remote_media":true}]}`
)

func testSite(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	t.Setenv("IFYTV_BASE_URL", s.URL)
	// Windows temporary folders may inherit ACLs granting other accounts
	// modify access. Create a private state root as a real fresh install does.
	root := filepath.Join(t.TempDir(), "state")
	if err := ensureStateDir(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IFYTV_STATE_DIR", root)
	t.Setenv("IFYTV_API_KEY", testKey)
	return s
}

type invocation struct {
	code             int
	out, diagnostics string
}

func invoke(ctx context.Context, args ...string) invocation {
	var out, diagnostics bytes.Buffer
	code := Execute(ctx, args, &out, &diagnostics, "0.1.0")
	return invocation{code, out.String(), diagnostics.String()}
}

func (v invocation) result(t *testing.T, success bool) map[string]any {
	t.Helper()
	if strings.Contains(v.out+v.diagnostics, testKey) {
		t.Fatal("credential exposed in command output")
	}
	if (v.code == 0) != success {
		t.Fatalf("exit %d, stdout %q, stderr %q", v.code, v.out, v.diagnostics)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(v.out), &obj); err != nil || obj == nil {
		t.Fatalf("expected one JSON object, got %q (%v), stderr %q", v.out, err, v.diagnostics)
	}
	if !success && obj["error"] == nil {
		t.Fatalf("missing error in %v", obj)
	}
	return obj
}

func jsonReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func created(w http.ResponseWriter) {
	jsonReply(w, 202, fmt.Sprintf(`{"id":%q,"status":"queued"}`, taskID))
}

func newSpec() map[string]any {
	return map[string]any{"model": "test-h3", "mode": "omni", "prompt": "角色一 & two <three>", "ratio": "16:9", "duration": 10}
}

func writeSpec(t *testing.T, dir string, spec map[string]any) string {
	t.Helper()
	file := filepath.Join(dir, "镜头 spec.json")
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func createRun(spec, run string) invocation {
	return invoke(context.Background(), "video", "create", "--no-canvas", "--spec", spec, "--run", run, "--json")
}

func TestVersionHelpAndPublicCatalog(t *testing.T) {
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected catalog request")
		}
		jsonReply(w, 200, catalogJSON)
	})
	t.Setenv("IFYTV_API_KEY", "")
	v := invoke(context.Background(), "--version")
	if v.code != 0 || !strings.Contains(v.out, "0.1.0") {
		t.Fatalf("version: %+v", v)
	}
	v = invoke(context.Background(), "--help")
	if v.code != 0 || !strings.Contains(v.out, "video create") {
		t.Fatalf("help: %+v", v)
	}
	models := invoke(context.Background(), "models", "--json").result(t, true)["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["max_audios"] != float64(3) {
		t.Fatal(models)
	}
}

func TestTaskTargetValidationAndCredentialDenial(t *testing.T) {
	var calls atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/device/start" && r.Header.Get("Authorization") == "" {
			jsonReply(w, 200, challengeJSON)
			return
		}
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("missing credential")
		}
		jsonReply(w, 401, fmt.Sprintf(`{"error":{"code":"invalid_api_key","message":"Bearer %s"}}`, testKey))
	})
	for _, args := range [][]string{
		{"video", "status", "--run", "x", "--task", taskID}, {"video", "wait", "--task", "../file"},
		{"video", "download", "--out", "out.mp4"}, {"canvas", "import", "--project", "bad", "--task", taskID},
		{"video", "create", "--run", "../x", "--spec", "unused"}, {"video", "status", "--task", taskID, "trailing"},
		{"canvas", "import", "--project", "00000000-0000-0000-0000-000000000000", "--task", taskID},
	} {
		invoke(context.Background(), append(args, "--json")...).result(t, false)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input made an HTTP request")
	}
	res := invoke(context.Background(), "video", "status", "--task", taskID, "--json").result(t, false)
	if res["error"].(map[string]any)["code"] != "invalid_api_key" || calls.Load() != 1 {
		t.Fatal(res, calls.Load())
	}
	t.Setenv("IFYTV_API_KEY", "")
	o := invoke(context.Background(), "canvas", "import", "--task", taskID, "--project", projectID, "--json").result(t, false)
	if o["task_id"] != taskID || o["project_id"] != projectID || o["error"].(map[string]any)["code"] != "authorization_required" {
		t.Fatal("missing original task/project on authentication failure", o)
	}
	if calls.Load() != 1 {
		t.Fatal("a protected endpoint was called before approval")
	}
}

func TestWaitTimeoutCoversRunResolution(t *testing.T) {
	s := testSite(t, func(w http.ResponseWriter, r *http.Request) { t.Error("wait should be blocked on the run lock") })
	c, problem := newClient(s.URL, testKey, true)
	if problem != nil {
		t.Fatal(problem)
	}
	holder, problem := c.lockRun(context.Background(), "locked")
	if problem != nil {
		t.Fatal(problem)
	}
	defer holder.lock.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	safety := time.AfterFunc(time.Second, cancel)
	defer safety.Stop()
	o := invoke(ctx, "video", "wait", "--run", "locked", "--timeout", "30ms", "--json").result(t, false)
	if o["error"].(map[string]any)["code"] != "timeout" {
		t.Fatal("wait timeout did not cover lock acquisition", o)
	}
}

func TestWaitResumesAfterRateLimitAndReportsTerminalFailure(t *testing.T) {
	var polls atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks/"+taskID || r.Method != "GET" {
			t.Error(r.URL.Path)
		}
		switch polls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			jsonReply(w, 429, `{"error":{"code":"rate_limit_exceeded"}}`)
		case 2:
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"processing","progress":58}`, taskID))
		case 3:
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","video_url":"https://other.invalid/?secret=internal"}`, taskID))
		default:
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"failed","error_code":"upstream_unavailable","error_message":"Bearer %s"}`, taskID, testKey))
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v := invoke(ctx, "video", "wait", "--task", taskID, "--interval", "2ms", "--json")
	o := v.result(t, true)
	if !strings.Contains(v.diagnostics, "processing") {
		t.Error("waiting progress missing from stderr")
	}
	if o["task_id"] != taskID || o["status"] != "succeeded" || strings.Contains(v.out, "secret=") || polls.Load() != 3 {
		t.Fatal(o)
	}
	o = invoke(ctx, "video", "wait", "--task", taskID, "--json").result(t, false)
	if o["status"] != "failed" || o["task_id"] != taskID || !strings.Contains(fmt.Sprint(o), "upstream_unavailable") {
		t.Fatal(o)
	}
}

func TestImportByTaskAndRunAndDeniedAuthorization(t *testing.T) {
	var imports atomic.Int32
	s := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			created(w)
		case "/api/v1/cli/projects/" + projectID + "/videos":
			if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Error("bad import headers or method")
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]string{"task_id": taskID}) {
				t.Errorf("import body %v", body)
			}
			count := imports.Add(1)
			if count == 1 {
				jsonReply(w, 403, fmt.Sprintf(`{"error":{"code":"cli_grant_required","message":"Bearer %s"},"authorization_url":"https://other.invalid/"}`, testKey))
				return
			}
			jsonReply(w, 200, fmt.Sprintf(`{"project_id":%q,"node_id":"video-abc","asset_id":%q,"project_url":"/canvas/cloud/%s","duplicate":%t}`, projectID, assetID, projectID, count == 3))
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	file := writeSpec(t, t.TempDir(), newSpec())
	createRun(file, "scene").result(t, true)
	command := []string{"canvas", "import", "--project", projectID}
	for i, target := range [][]string{{"--task", taskID}, {"--run", "scene"}, {"--task", taskID}} {
		args := append(append([]string{}, command...), target...)
		o := invoke(context.Background(), append(args, "--json")...).result(t, i > 0)
		if o["task_id"] != taskID || o["project_id"] != projectID {
			t.Fatal(o)
		}
		if i == 0 {
			if o["authorization_url"] != s.URL+"/cli?project="+projectID {
				t.Fatal(o)
			}
		} else if o["node_id"] != "video-abc" || o["asset_id"] != assetID || o["duplicate"] != (i == 2) || o["project_url"] != s.URL+"/canvas/cloud/"+projectID {
			t.Fatal(o)
		}
	}
}
