package ifytvcli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const canvasAssetTestEndpoint = "/api/v1/cli/projects/" + projectID + "/assets/" + assetID + "/download"

func canvasAssetTestOptions(t *testing.T, extension string) options {
	t.Helper()
	return options{
		project: projectID, asset: assetID, authorizationID: productionTestConsent,
		out: filepath.Join(t.TempDir(), "画布素材"+extension),
	}
}

func writeCanvasAssetTestHeaders(w http.ResponseWriter, data []byte, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-IFYTV-Asset-SHA256", strings.ToUpper(digest(data)))
}

func assertCanvasAssetNoFiles(t *testing.T, directory string) {
	t.Helper()
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected download left files: %v, %v", files, err)
	}
}

func TestCanvasAssetFetchSavesVerifiedBytes(t *testing.T) {
	png := productionTestPNG(t)
	for _, tc := range []struct {
		extension, contentType string
		data                   []byte
	}{
		{".PNG", "image/png", png},
		{".jpg", "image/jpeg", []byte("jpeg fixture")},
		{".gif", "image/gif", []byte("gif fixture")},
		{".webp", "image/webp", []byte("webp fixture")},
		{".mp3", "audio/mpeg", []byte("mp3 fixture")},
		{".wav", "audio/x-wav", []byte("wav fixture")},
		{".m4a", "audio/mp4", []byte("m4a fixture")},
		{".aac", "audio/aac", []byte("aac fixture")},
		{".oga", "audio/ogg", []byte("ogg fixture")},
		{".mp4", "video/mp4", []byte("mp4 fixture")},
		{".webm", "audio/webm", []byte("webm fixture")},
		{".mov", "video/quicktime", []byte("mov fixture")},
		{".txt", "text/plain; charset=UTF-8", []byte("脚本")},
		{".md", "text/markdown", []byte("# 场景")},
	} {
		t.Run(tc.extension, func(t *testing.T) {
			o := canvasAssetTestOptions(t, tc.extension)
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != canvasAssetTestEndpoint ||
					r.Header.Get("Authorization") != "Bearer "+deviceToken ||
					r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Range") != "" {
					t.Errorf("unexpected asset download request: %s %s", r.Method, r.URL)
				}
				writeCanvasAssetTestHeaders(w, tc.data, tc.contentType)
				w.Header().Set("Content-Disposition", "attachment; filename=ignored.exe")
				_, _ = w.Write(tc.data)
			})
			got, problem := c.fetchCanvasAsset(context.Background(), o)
			if problem != nil || got.AssetID != assetID || got.ProjectID != projectID ||
				got.ProjectURL != c.base.String()+"/canvas/cloud/"+projectID || got.Path != o.out || got.Bytes != int64(len(tc.data)) {
				t.Fatalf("asset receipt %#v, failure %v", got, problem)
			}
			data, err := os.ReadFile(got.Path)
			if err != nil || !bytes.Equal(data, tc.data) {
				t.Fatalf("saved bytes %q, error %v", data, err)
			}
			files, err := os.ReadDir(filepath.Dir(o.out))
			if err != nil || len(files) != 1 {
				t.Fatalf("output directory has unexpected files: %v, %v", files, err)
			}
		})
	}
}

func TestCanvasAssetFetchRejectsInvalidResponses(t *testing.T) {
	data := []byte("素材说明")
	for _, tc := range []struct {
		name, wantCode string
		change         func(http.ResponseWriter)
	}{
		{"wrong checksum", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("X-IFYTV-Asset-SHA256", digest([]byte("different bytes")))
		}},
		{"missing checksum", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Del("X-IFYTV-Asset-SHA256")
		}},
		{"duplicate checksum", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Add("X-IFYTV-Asset-SHA256", digest(data))
		}},
		{"malformed checksum", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("X-IFYTV-Asset-SHA256", strings.Repeat("z", 64))
		}},
		{"truncated", "download_incomplete", func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
		}},
		{"missing length", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Del("Content-Length")
			w.(http.Flusher).Flush()
		}},
		{"oversize", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "2147483649")
		}},
		{"wrong MIME", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "image/png")
		}},
		{"generic MIME", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/octet-stream")
		}},
		{"wrong charset", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/plain; charset=iso-8859-1")
		}},
		{"encoded", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Encoding", "gzip")
		}},
		{"partial", "invalid_download", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusPartialContent)
		}},
		{"content range", "invalid_download", func(w http.ResponseWriter) {
			w.Header().Set("Content-Range", "bytes 0-2/3")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := canvasAssetTestOptions(t, ".txt")
			c := productionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeCanvasAssetTestHeaders(w, data, "text/plain")
				tc.change(w)
				_, _ = w.Write(data)
			})
			got, problem := c.fetchCanvasAsset(context.Background(), o)
			if problem == nil || problem.Code != tc.wantCode || got.Path != "" || got.Bytes != 0 ||
				got.AssetID != assetID || got.ProjectID != projectID {
				t.Fatalf("invalid download outcome %#v, failure %v", got, problem)
			}
			assertCanvasAssetNoFiles(t, filepath.Dir(o.out))
		})
	}
}

func TestCanvasAssetFetchRejectsExistingAndUnsafePathsBeforeRequest(t *testing.T) {
	o := canvasAssetTestOptions(t, ".txt")
	original := []byte("已有文件")
	productionTestFile(t, o.out, original)
	c := productionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unsafe or existing output reached the site")
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, tc := range []struct{ name, path, code string }{
		{"existing", o.out, "output_exists"},
		{"executable", filepath.Join(filepath.Dir(o.out), "asset.exe"), "invalid_output"},
		{"https destination", "https://private.example/asset.jpg", "invalid_output"},
		{"file URL", "file:/asset.jpg", "invalid_output"},
		{"UNC", "\\\\example.invalid\\share\\asset.jpg", "invalid_output"},
		{"mixed network prefix", "\\/example.invalid/share/asset.jpg", "invalid_output"},
		{"alternate stream", filepath.Join(filepath.Dir(o.out), "asset.txt:private.jpg"), "invalid_output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := o
			attempt.out = tc.path
			if got, problem := c.fetchCanvasAsset(context.Background(), attempt); problem == nil || problem.Code != tc.code || got.Path != "" {
				t.Fatalf("unsafe output outcome %#v, failure %v", got, problem)
			}
		})
	}
	got, err := os.ReadFile(o.out)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("original file changed: %q, %v", got, err)
	}
}

func TestCanvasAssetFetchPreservesConcurrentOutput(t *testing.T) {
	o := canvasAssetTestOptions(t, ".txt")
	data, existing := []byte("downloaded"), []byte("concurrent writer")
	c := productionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if err := os.WriteFile(o.out, existing, 0600); err != nil {
			t.Error(err)
		}
		writeCanvasAssetTestHeaders(w, data, "text/plain")
		_, _ = w.Write(data)
	})
	got, problem := c.fetchCanvasAsset(context.Background(), o)
	if problem == nil || problem.Code != "output_exists" || got.Path != "" {
		t.Fatalf("concurrent output outcome %#v, failure %v", got, problem)
	}
	actual, err := os.ReadFile(o.out)
	if err != nil || !bytes.Equal(actual, existing) {
		t.Fatalf("concurrent output changed: %q, %v", actual, err)
	}
	files, err := os.ReadDir(filepath.Dir(o.out))
	if err != nil || len(files) != 1 {
		t.Fatalf("output directory has unexpected files: %v, %v", files, err)
	}
}

func TestCanvasAssetFetchRejectsRedirectsBeforeDestinationRequest(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(foreign.Close)
	for _, tc := range []struct{ name, target string }{
		{"foreign origin", foreign.URL + canvasAssetTestEndpoint},
		{"other project", strings.Replace(canvasAssetTestEndpoint, projectID, taskID, 1)},
		{"other asset", strings.Replace(canvasAssetTestEndpoint, assetID, taskID, 1)},
		{"signed same-origin URL", canvasAssetTestEndpoint + "?signed=secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := canvasAssetTestOptions(t, ".txt")
			var siteCalls atomic.Int32
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				siteCalls.Add(1)
				if r.URL.RequestURI() != canvasAssetTestEndpoint {
					t.Errorf("redirect destination reached: %s", r.URL.RequestURI())
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, tc.target, http.StatusFound)
			})
			got, problem := c.fetchCanvasAsset(context.Background(), o)
			if problem == nil || problem.Code != "redirect_rejected" || got.Path != "" ||
				siteCalls.Load() != 1 || foreignCalls.Load() != 0 {
				t.Fatalf("redirect outcome %#v, failure %v, site requests %d, foreign requests %d", got, problem, siteCalls.Load(), foreignCalls.Load())
			}
			assertCanvasAssetNoFiles(t, filepath.Dir(o.out))
		})
	}
}

func TestCanvasAssetFetchRequiresPinnedDeviceBeforeRequest(t *testing.T) {
	for _, tc := range []struct{ name, key, scope, consent string }{
		{"API key", testKey, productionTestScope, productionTestConsent},
		{"no key", "", productionTestScope, productionTestConsent},
		{"no saved account scope", deviceToken, "", productionTestConsent},
		{"no pinned approval", deviceToken, productionTestScope, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := canvasAssetTestOptions(t, ".png")
			o.authorizationID = tc.consent
			c := productionTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("request reached site without required device approval")
				w.WriteHeader(http.StatusInternalServerError)
			})
			c.key, c.runScope = tc.key, tc.scope
			got, problem := c.fetchCanvasAsset(context.Background(), o)
			if problem == nil || problem.Code != "device_login_required" || got.Path != "" ||
				got.AssetID != assetID || got.ProjectURL != c.base.String()+"/canvas/cloud/"+projectID {
				t.Fatalf("authorization outcome %#v, failure %v", got, problem)
			}
			assertCanvasAssetNoFiles(t, filepath.Dir(o.out))
		})
	}
}

func TestCanvasAssetFetchPreservesProjectScopeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, project, code string
		status              int
		consent             bool
	}{
		{"expired device", projectID, "device_unauthorized", http.StatusUnauthorized, false},
		{"device scope denied", projectID, "device_scope_denied", http.StatusForbidden, false},
		{"project grant required", projectID, "cli_grant_required", http.StatusForbidden, true},
		{"asset from a different project", taskID, "canvas_asset_not_found", http.StatusNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := canvasAssetTestOptions(t, ".png")
			o.project = tc.project
			c := productionTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				want := "/api/v1/cli/projects/" + tc.project + "/assets/" + assetID + "/download"
				if r.URL.RequestURI() != want {
					t.Errorf("request escaped selected project: %s", r.URL.RequestURI())
				}
				jsonReply(w, tc.status, fmt.Sprintf("{\"error\":{\"code\":%q,\"message\":\"https://private.example/asset?signature=secret\"}}", tc.code))
			})
			got, problem := c.fetchCanvasAsset(context.Background(), o)
			wantConsent := ""
			if tc.consent {
				wantConsent = c.base.String() + "/cli?project=" + tc.project
			}
			if problem == nil || problem.Code != tc.code || problem.HTTPStatus != tc.status ||
				strings.Contains(problem.Message, "signature") || got.AuthorizationURL != wantConsent || got.AssetID != assetID ||
				got.ProjectURL != c.base.String()+"/canvas/cloud/"+tc.project || got.Path != "" {
				t.Fatalf("authorization outcome %#v, failure %v", got, problem)
			}
			assertCanvasAssetNoFiles(t, filepath.Dir(o.out))
		})
	}
}

func TestCanvasAssetFetchCommandUsesOriginalDeviceApproval(t *testing.T) {
	data := []byte("一段分镜脚本")
	var downloads atomic.Int32
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			jsonReply(w, http.StatusOK, challengeJSON)
		case "/api/v1/auth/device/token":
			deviceReply(w, deviceToken, productionTestScope)
		case canvasAssetTestEndpoint:
			downloads.Add(1)
			if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer "+deviceToken {
				t.Error("asset request differs from the pinned project download")
			}
			writeCanvasAssetTestHeaders(w, data, "text/plain; charset=utf-8")
			_, _ = w.Write(data)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	login := authResult(t, authCommand("login"), true, "authorization_required")
	pin := digest([]byte(deviceSecret))
	if login["authorization_id"] != pin {
		t.Fatal("login did not identify the displayed approval")
	}
	authResult(t, authCommand("wait", "--authorization-id", pin, "--timeout", "5s"), true, "authenticated")
	o := canvasAssetTestOptions(t, ".txt")
	projectURL := site.URL + "/canvas/cloud/" + projectID
	fetch := func(approval string) invocation {
		return invoke(context.Background(), "canvas", "fetch", "--project", projectURL+"?node=existing-card",
			"--asset", assetID, "--out", o.out, "--authorization-id", approval, "--json")
	}
	expectError(t, fetch(productionTestConsent).result(t, false), "authorization_changed")
	if downloads.Load() != 0 {
		t.Fatal("a different approval reached the asset route")
	}
	assertCanvasAssetNoFiles(t, filepath.Dir(o.out))

	run := fetch(pin)
	result := run.result(t, true)
	if downloads.Load() != 1 || result["asset_id"] != assetID || result["project_url"] != projectURL ||
		result["path"] != o.out || result["bytes"] != float64(len(data)) ||
		strings.Contains(run.out+run.diagnostics, deviceToken) {
		t.Fatal("unexpected command receipt", result)
	}
	got, err := os.ReadFile(o.out)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded bytes %q, error %v", got, err)
	}
}
