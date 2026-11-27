package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const productionTestConsent = "c333333333333333333333333333333333333333333333333333333333333333"
const productionTestScope = "e333333333333333333333333333333333333333333333333333333333333333"

const productionTestCapabilities = `{"image_models":["draw/v1"],"voices":[{"id":"voice-A","name":"旁白"}],"publish_episode":true}`
const productionTestTasks = "/api/v1/cli/projects/" + projectID + "/media/tasks"
const productionTestUploads = "/api/v1/cli/projects/" + projectID + "/media/uploads"

func productionTestClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	c := canvasHTTPClient(t, deviceToken, handler)
	c.runScope = productionTestScope
	return c
}

func productionTestFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func productionTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 20, 15))
	img.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func productionTestReply(status, kind string) string {
	return fmt.Sprintf(`{"task_id":%q,"kind":%q,"status":%q}`, taskID, kind, status)
}

func productionTestOptions(t *testing.T, spec, extension string) options {
	t.Helper()
	dir := t.TempDir()
	return options{
		project: projectID, run: "成片 01", spec: productionTestFile(t, filepath.Join(dir, "media.json"), []byte(spec)),
		out: filepath.Join(dir, "asset"+extension), authorizationID: productionTestConsent, interval: time.Millisecond, timeout: time.Second,
	}
}

func TestProductionMediaReplaysLostReceiptWithFreshReferenceURL(t *testing.T) {
	img := productionTestPNG(t)
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"夜色中漫步","ratio":"9:16","references":["reference.png"]}`, ".png")
	productionTestFile(t, filepath.Join(filepath.Dir(o.spec), "reference.png"), img)

	var uploads, creates, shells atomic.Int32
	var firstKey atomic.Value
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ifytv_device_") {
			t.Error("request is missing device authorization")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/cli/media/capabilities":
			jsonReply(w, 200, productionTestCapabilities)
		case "POST " + productionTestUploads:
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			body, readErr := io.ReadAll(f)
			_ = f.Close()
			if readErr != nil || !bytes.Equal(body, img) {
				t.Error("incorrect reference bytes")
			}
			uploadNumber := uploads.Add(1)
			jsonReply(w, 200, fmt.Sprintf(`{"url":%q,"media_type":"image","size":%d}`, productionTestUploadURL(r.Host, uploadNumber), len(img)))
		case "POST " + productionTestTasks:
			var payload struct {
				Kind            string   `json:"kind"`
				Model           string   `json:"model"`
				Prompt          string   `json:"prompt"`
				Ratio           string   `json:"ratio"`
				References      []string `json:"references"`
				ReferenceSHA256 []string `json:"reference_sha256"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if payload.Kind != "image" || payload.Model != "draw/v1" || payload.Prompt != "夜色中漫步" || payload.Ratio != "9:16" {
				t.Errorf("invalid image parameters: %+v", payload)
			}
			expectedURL := productionTestUploadURL(r.Host, uploads.Load())
			if len(payload.References) != 1 || payload.References[0] != expectedURL || len(payload.ReferenceSHA256) != 1 || payload.ReferenceSHA256[0] != digest(img) {
				t.Errorf("incorrect references: %+v", payload)
			}
			key := r.Header.Get("Idempotency-Key")
			if !validRunScope(key) {
				t.Errorf("unexpected production key %q", key)
			}
			if creates.Add(1) == 1 {
				firstKey.Store(key)
				shells.Add(1)
				jsonReply(w, 201, `{"task_id":`)
				return
			}
			if key != firstKey.Load() {
				shells.Add(1)
				t.Error("recovery used a different production key")
			}
			jsonReply(w, 200, productionTestReply("queued", "image"))
		case "GET " + productionTestTasks + "/" + taskID:
			jsonReply(w, 200, productionTestReply("succeeded", "image"))
		case "GET " + productionTestTasks + "/" + taskID + "/download":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", fmt.Sprint(len(img)))
			_, _ = w.Write(img)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	result, problem := c.generateProductionMedia(context.Background(), o)
	if problem == nil || problem.Code != "invalid_response" || result.ProjectURL != c.base.String()+"/canvas/cloud/"+projectID {
		t.Fatalf("lost reply result %+v, problem %+v", result, problem)
	}
	if _, err := os.Stat(o.out); !os.IsNotExist(err) {
		t.Fatalf("output after lost reply: %v", err)
	}

	c.key = "ifytv_device_refreshed-test-bearer"
	o.authorizationID = digest([]byte("a new login in the same scope"))
	result, problem = c.generateProductionMedia(context.Background(), o)
	if problem != nil || result.TaskID != taskID || result.Status != "succeeded" || result.Path != o.out || result.Bytes != int64(len(img)) {
		t.Fatalf("recovery result %+v, problem %+v", result, problem)
	}
	body, err := os.ReadFile(o.out)
	if err != nil || !bytes.Equal(body, img) || creates.Load() != 2 || shells.Load() != 1 {
		t.Fatalf("recovery by replay created %d jobs from %d requests, file error %v", shells.Load(), creates.Load(), err)
	}
}

func TestProductionMediaChecksAccessAndCapabilityBeforeMutation(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"speech","voice":"voice-A","text":"旁白"}`, ".mp3")
	var requests atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/cli/media/capabilities" {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		jsonReply(w, 200, `{"image_models":[],"voices":[],"publish_episode":false}`)
	})
	missing := o
	missing.authorizationID = ""
	for _, tc := range []struct {
		name           string
		args           options
		bearer, reason string
		wantRequests   int32
	}{
		{"no approval", missing, deviceToken, "device_login_required", 0},
		{"video key", o, "video-only-key", "device_login_required", 0},
		{"voice disabled", o, deviceToken, "media_capability_unavailable", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.key = tc.bearer
			_, problem := c.generateProductionMedia(context.Background(), tc.args)
			if problem == nil || problem.Code != tc.reason || requests.Load() != tc.wantRequests {
				t.Fatalf("got problem %+v and %d requests", problem, requests.Load())
			}
		})
	}
	if _, err := os.Stat(o.out); !os.IsNotExist(err) {
		t.Fatalf("output with disabled voice: %v", err)
	}
}

func TestProductionMediaRejectsMalformedMP3AndRetainsTask(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"speech","voice":"voice-A","text":"旁白","emotion":"calm"}`, ".mp3")
	// Two structurally complete 128kbps 44.1kHz MPEG 1 Layer III frames.
	frame := make([]byte, 417)
	copy(frame, []byte{0xff, 0xfb, 0x90, 0x64})
	sample := append(bytes.Clone(frame), frame...)
	results := []struct {
		name        string
		data        []byte
		wantSuccess bool
	}{
		{"html advertised as MP3", []byte("<html>download error</html>"), false},
		{"partial ID3 header", []byte("ID3"), false},
		{"missing audio tail despite exact HTTP length", sample[:len(sample)-1], false},
		{"complete MPEG audio", sample, true},
	}
	for _, tc := range results {
		t.Run(tc.name, func(t *testing.T) {
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/cli/media/capabilities":
					jsonReply(w, 200, productionTestCapabilities)
				case "POST " + productionTestTasks:
					var got map[string]string
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got["kind"] != "speech" || got["voice"] != "voice-A" || got["text"] != "旁白" || got["emotion"] != "calm" {
						t.Errorf("incorrect speech request %v, error %v", got, err)
					}
					jsonReply(w, 201, productionTestReply("succeeded", "speech"))
				case "GET " + productionTestTasks + "/" + taskID + "/download":
					w.Header().Set("Content-Type", "application/octet-stream")
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.data)))
					_, _ = w.Write(tc.data)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			result, problem := c.generateProductionMedia(context.Background(), o)
			if result.TaskID != taskID || result.ProjectURL == "" {
				t.Fatalf("lost task metadata: %+v", result)
			}
			if tc.wantSuccess {
				if problem != nil {
					t.Fatal(problem)
				}
				body, err := os.ReadFile(o.out)
				if !bytes.Equal(body, tc.data) || err != nil {
					t.Fatalf("bad output, file error %v", err)
				}
				return
			}
			if problem == nil || problem.Code != "invalid_download" || result.Path != "" {
				t.Fatalf("malformed audio result %+v, problem %+v", result, problem)
			}
			files, err := os.ReadDir(filepath.Dir(o.out))
			if err != nil || len(files) != 1 {
				t.Fatalf("partial output survived: %v, error %v", files, err)
			}
		})
	}
}

func TestProductionMediaPublishConfirmsProjectAndReusesReceiptKey(t *testing.T) {
	dir := t.TempDir()
	o := options{
		project:         projectID,
		run:             "夜归 第一集",
		file:            productionTestFile(t, filepath.Join(dir, "final.mp4"), mp4Fixture),
		authorizationID: productionTestConsent,
	}
	var posts atomic.Int32
	var firstKey atomic.Value
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/cli/media/capabilities":
			jsonReply(w, 200, productionTestCapabilities)
		case "POST " + productionTestUploads:
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			body, readErr := io.ReadAll(f)
			_ = f.Close()
			if readErr != nil || !bytes.Equal(body, mp4Fixture) {
				t.Error("upload differed from the final cut")
			}
			jsonReply(w, 200, fmt.Sprintf(`{"url":%q,"media_type":"video"}`, "http://"+r.Host+productionTestUploads+"/"+assetID))
		case "POST /api/v1/cli/projects/" + projectID + "/media/episodes":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["title"] != o.run || payload["video_sha256"] != digest(mp4Fixture) || payload["video_url"] != "http://"+r.Host+productionTestUploads+"/"+assetID {
				t.Errorf("unexpected publish payload: %v, error %v", payload, err)
			}
			switch posts.Add(1) {
			case 1:
				firstKey.Store(r.Header.Get("Idempotency-Key"))
				jsonReply(w, 403, `{"error":{"code":"cli_grant_required"}}`)
			case 2:
				if firstKey.Load() != r.Header.Get("Idempotency-Key") {
					t.Error("changed publish key")
				}
				jsonReply(w, 201, fmt.Sprintf(`{"project_id":%q,"node_id":"episode-1","asset_id":%q}`, assetID, assetID))
			case 3:
				jsonReply(w, 200, fmt.Sprintf(`{"project_id":%q,"node_id":"episode-1","asset_id":%q,"duplicate":true}`, projectID, assetID))
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	result, problem := c.publishEpisode(context.Background(), o)
	if problem == nil || problem.Code != "cli_grant_required" || result.AuthorizationURL != c.base.String()+"/cli?project="+projectID {
		t.Fatalf("grant result %+v, problem %+v", result, problem)
	}
	result, problem = c.publishEpisode(context.Background(), o)
	if problem == nil || problem.Code != "invalid_response" || result.NodeID != "" {
		t.Fatalf("mismatched project result %+v, problem %+v", result, problem)
	}
	result, problem = c.publishEpisode(context.Background(), o)
	if problem != nil || result.ProjectID != projectID || result.NodeID != "episode-1" || result.AssetID != assetID || result.Duplicate == nil || !*result.Duplicate || result.ProjectURL != c.base.String()+"/canvas/cloud/"+projectID+"?node=episode-1" {
		t.Fatalf("publish result %+v, problem %+v", result, problem)
	}
}

func TestProductionMediaRejectsDownloadRedirectToAnotherOrigin(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"房间","ratio":"16:9"}`, ".png")
	img := productionTestPNG(t)
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	}))
	defer foreign.Close()
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/cli/media/capabilities":
			jsonReply(w, 200, productionTestCapabilities)
		case "POST " + productionTestTasks:
			jsonReply(w, 201, productionTestReply("succeeded", "image"))
		case "GET " + productionTestTasks + "/" + taskID + "/download":
			http.Redirect(w, r, foreign.URL, http.StatusFound)
		default:
			t.Error("unexpected request", r.URL)
			http.NotFound(w, r)
		}
	})
	result, problem := c.generateProductionMedia(context.Background(), o)
	if problem == nil || problem.Code != "redirect_rejected" || foreignCalls.Load() != 0 || result.TaskID != taskID {
		t.Fatalf("redirect result %+v, problem %+v, foreign calls %d", result, problem, foreignCalls.Load())
	}
}

func TestProductionMediaPreservesTaskOnPollDeadline(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"窗","ratio":"1:1"}`, ".png")
	var polls atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/cli/media/capabilities":
			jsonReply(w, 200, productionTestCapabilities)
		case "POST " + productionTestTasks:
			jsonReply(w, 201, productionTestReply("queued", "image"))
		case "GET " + productionTestTasks + "/" + taskID:
			polls.Add(1)
			w.Header().Set("Retry-After", "10")
			jsonReply(w, 429, `{"error":{"code":"rate_limited"}}`)
		default:
			t.Error("unexpected request", r.URL)
			http.NotFound(w, r)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, problem := c.generateProductionMedia(ctx, o)
	if problem == nil || problem.Code != "timeout" || result.TaskID != taskID || result.Status != "queued" || result.ProjectURL == "" || polls.Load() != 1 {
		t.Fatalf("deadline result %+v, problem %+v, polls %d", result, problem, polls.Load())
	}
}

func TestProductionMediaPreflightsTheDestination(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"窗","ratio":"1:1"}`, ".png")
	existing := []byte("another writer's artifact")
	productionTestFile(t, o.out, existing)
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("must preflight output before contacting the site, got %s", r.URL)
		http.NotFound(w, r)
	})
	_, problem := c.generateProductionMedia(context.Background(), o)
	if problem == nil || problem.Code != "output_exists" {
		t.Fatalf("got problem %+v", problem)
	}
	if got, err := os.ReadFile(o.out); err != nil || !bytes.Equal(got, existing) {
		t.Fatalf("overwrote existing output, error %v", err)
	}
}

func TestProductionMediaKeysSeparateAccountsAndOperations(t *testing.T) {
	o := options{run: "same", project: projectID}
	c, problem := newClient("https://example.invalid", deviceToken, true)
	if problem != nil {
		t.Fatal(problem)
	}
	c.runScope = productionTestScope
	original := c.productionKey("generate", o)
	if c.productionKey("publish", o) == original {
		t.Fatal("generation and publication share a key")
	}
	renamed := o
	renamed.run = "new run"
	if c.productionKey("generate", renamed) == original {
		t.Fatal("a new run has the previous key")
	}
	other := o
	other.project = assetID
	if c.productionKey("generate", other) == original {
		t.Fatal("another project shares the key")
	}
	c.runScope = digest([]byte("other account"))
	if c.productionKey("generate", o) == original {
		t.Fatal("another account shares the key")
	}
}

func TestProductionMediaStagingRequiresPermissionBeforeAnyTask(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"窗","ratio":"1:1","references":["reference.png"]}`, ".png")
	productionTestFile(t, filepath.Join(filepath.Dir(o.spec), "reference.png"), productionTestPNG(t))
	o.file = productionTestFile(t, filepath.Join(filepath.Dir(o.spec), "episode.mp4"), mp4Fixture)
	var staged atomic.Int32
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/cli/media/capabilities":
			jsonReply(w, 200, productionTestCapabilities)
		case "POST " + productionTestUploads:
			staged.Add(1)
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
			} else {
				_ = f.Close()
			}
			jsonReply(w, 403, `{"error":{"code":"cli_grant_required"}}`)
		default:
			t.Errorf("no general staging or task POST after denied project staging, got %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	for _, attempt := range []func(context.Context, options) (output, *failure){c.generateProductionMedia, c.publishEpisode} {
		result, problem := attempt(context.Background(), o)
		if problem == nil || problem.Code != "cli_grant_required" || result.AuthorizationURL != c.base.String()+"/cli?project="+projectID || result.TaskID != "" || result.NodeID != "" {
			t.Fatalf("denied staging result %+v, problem %+v", result, problem)
		}
	}
	if staged.Load() != 2 {
		t.Fatalf("expected two project scoped uploads, got %d", staged.Load())
	}
}

func TestProductionMediaRejectsUnscopedAndSignedUploadReceipts(t *testing.T) {
	o := productionTestOptions(t, `{"kind":"image","model":"draw/v1","prompt":"窗","ratio":"1:1","references":["reference.png"]}`, ".png")
	productionTestFile(t, filepath.Join(filepath.Dir(o.spec), "reference.png"), productionTestPNG(t))
	for _, tc := range []struct {
		name    string
		receipt func(string) string
	}{
		{"public path", func(host string) string { return "http://" + host + "/api/v1/uploads/" + assetID }},
		{"foreign origin", func(host string) string { return "https://example.invalid" + productionTestUploads + "/" + assetID }},
		{"other project", func(host string) string {
			return "http://" + host + strings.Replace(productionTestUploads, projectID, assetID, 1) + "/" + assetID
		}},
		{"signed URL", func(host string) string {
			return "http://" + host + productionTestUploads + "/" + assetID + "?sig=value"
		}},
		{"noncanonical path", func(host string) string { return "http://" + host + productionTestUploads + "/%64" + assetID[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var staged atomic.Int32
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/cli/media/capabilities":
					jsonReply(w, 200, productionTestCapabilities)
				case "POST " + productionTestUploads:
					staged.Add(1)
					f, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
					} else {
						_ = f.Close()
					}
					jsonReply(w, 200, fmt.Sprintf(`{"url":%q,"media_type":"image"}`, tc.receipt(r.Host)))
				default:
					t.Errorf("unexpected request with invalid receipt: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			result, problem := c.generateProductionMedia(context.Background(), o)
			if problem == nil || problem.Code != "invalid_upload_response" || result.TaskID != "" || staged.Load() != 1 {
				t.Fatalf("invalid staging result %+v, problem %+v, staging requests %d", result, problem, staged.Load())
			}
		})
	}
}

func TestProductionMediaFetchCompletesExistingTaskAfterCapabilityWithdrawal(t *testing.T) {
	dir := t.TempDir()
	o := options{
		project: projectID, task: taskID, kind: "image", out: filepath.Join(dir, "resumed.png"),
		authorizationID: productionTestConsent, interval: time.Millisecond, timeout: time.Second,
	}
	img := productionTestPNG(t)
	var polls atomic.Int32
	path := productionTestTasks + "/" + taskID
	c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET " + path:
			status := "succeeded"
			if polls.Add(1) == 1 {
				status = "running"
			}
			jsonReply(w, 200, productionTestReply(status, "image"))
		case "GET " + path + "/download":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", fmt.Sprint(len(img)))
			_, _ = w.Write(img)
		default:
			t.Errorf("fetch should only query an existing task and download its asset, got %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	result, problem := c.fetchProductionMedia(context.Background(), o)
	data, err := os.ReadFile(o.out)
	if problem != nil || result.TaskID != taskID || result.ProjectID != projectID || result.Status != "succeeded" ||
		result.Path != o.out || polls.Load() != 2 || err != nil || !bytes.Equal(data, img) {
		t.Fatalf("fetch result %+v, problem %+v, polls %d, asset read error %v", result, problem, polls.Load(), err)
	}
}

func productionTestUploadURL(host string, n int32) string {
	return fmt.Sprintf("http://%s%s/e28c2074-bf62-4538-baba-%012d", host, productionTestUploads, n)
}
