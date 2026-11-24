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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestResumeRefreshesOnlyExpiredLocalUploadsBeforeCreate(t *testing.T) {
	const signature = "fixture-only-upload-signature"
	var probes atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Idempotency-Key") != "" || r.Referer() != "" {
			t.Error("credentials or referrer reached the CDN")
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Range") != "bytes=0-0" || r.URL.Query().Get("sig") != signature {
			t.Error("expected a bounded read of the original signed receipt")
		}
		probes.Add(1)
		switch r.URL.Path {
		case "/objects/keep.png/1":
			// This CDN ignores Range. The original local file has been removed.
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "\x89PNG\r\n\x1a\nkeep")
		case "/objects/gone.png/1":
			w.WriteHeader(http.StatusNotFound)
		case "/objects/voice.mp3/1":
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Error("probed a user URL, a new receipt, or an unexpected slot")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer cdn.Close()
	dir := t.TempDir()
	for _, name := range []string{"keep.png", "gone.png"} {
		localImage(t, dir, name, name)
	}
	for _, name := range []string{"voice.mp3", "tail.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ID3"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	remote := cdn.URL + "/user-input?sig=" + signature
	spec := newSpec()
	spec["images"] = []string{"keep.png", remote, "gone.png"}
	spec["audio"] = []string{"voice.mp3", "tail.mp3"}
	file := writeSpec(t, dir, spec)
	var mu sync.Mutex
	uploads := make(map[string]int)
	billed := make(map[string][]byte)
	var creates int
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		receipt := func(name string, attempt int) string {
			return fmt.Sprintf("http://%s/objects/%s/%d?sig=%s", r.Host, name, attempt, signature)
		}
		switch {
		case r.URL.Path == "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case r.URL.Path == "/api/v1/uploads":
			name, typ := receiveUpload(t, r)
			mu.Lock()
			uploads[name]++
			attempt := uploads[name]
			mu.Unlock()
			if name == "tail.mp3" && attempt == 1 {
				jsonReply(w, 503, "{}")
				return
			}
			uploadReceipt(w, receipt(name, attempt), strings.SplitN(typ, "/", 2)[0])
		case strings.HasPrefix(r.URL.Path, "/objects/"):
			if r.Header.Get("Authorization") != "" {
				t.Error("receipt probe authenticated even on the gateway origin")
			}
			http.Redirect(w, r, cdn.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		case r.URL.Path == "/api/v1/tasks":
			data, err := io.ReadAll(r.Body)
			var got videoSpec
			if err != nil || json.Unmarshal(data, &got) != nil ||
				!slices.Equal(got.Images, []string{receipt("keep.png", 1), remote, receipt("gone.png", 2)}) ||
				!slices.Equal(got.Audio, []string{receipt("voice.mp3", 2), receipt("tail.mp3", 2)}) {
				t.Error("create used an expired receipt, replaced a valid one, or changed reference order")
				jsonReply(w, 400, "{}")
				return
			}
			key := r.Header.Get("Idempotency-Key")
			mu.Lock()
			defer mu.Unlock()
			creates++
			if key == "" {
				t.Error("missing create key")
			}
			if original, exists := billed[key]; exists {
				if !bytes.Equal(original, data) {
					t.Error("replayed create changed its persisted request bytes")
				}
				created(w)
				return
			}
			billed[key] = data
			jsonReply(w, 202, "{") // Accepted and charged, but the client lost the usable reply.
		default:
			t.Error("unexpected gateway request")
		}
	})
	attempt := func() invocation {
		v := createRun(file, "resume")
		if strings.Contains(v.out+v.diagnostics, signature) {
			t.Fatal("signed media URL leaked")
		}
		return v
	}
	attempt().result(t, false)
	if probes.Load() != 0 {
		t.Fatal("first invocation probed newly uploaded references")
	}
	if err := os.Remove(filepath.Join(dir, "keep.png")); err != nil {
		t.Fatal(err)
	}
	o := attempt().result(t, false)
	if o["error"].(map[string]any)["code"] != "invalid_response" || probes.Load() != 3 {
		t.Fatalf("expected refreshed slots before the ambiguous create, probes=%d, error=%v", probes.Load(), o["error"])
	}
	attempt().result(t, true) // Prepared: replay exactly once with the existing billable key.
	attempt().result(t, true) // Submitted: return the persisted task.
	mu.Lock()
	defer mu.Unlock()
	if creates != 2 || len(billed) != 1 || probes.Load() != 3 ||
		!reflect.DeepEqual(uploads, map[string]int{"keep.png": 1, "gone.png": 2, "voice.mp3": 2, "tail.mp3": 2}) {
		t.Fatalf("creates=%d charges=%d probes=%d uploads=%v", creates, len(billed), probes.Load(), uploads)
	}
}

func TestResumeUnverifiedUploadPreservesCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		localAbsent bool
		body        string
	}{
		{name: "CDN unavailable", status: http.StatusServiceUnavailable},
		{name: "CDN limited", status: http.StatusTooManyRequests},
		{name: "empty media response", status: http.StatusOK},
		{name: "CDN error page", status: http.StatusOK, body: "<html>Unavailable</html>"},
		{name: "connection lost"},
		{name: "expired and local file missing", status: http.StatusGone, localAbsent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var checks, writes atomic.Int32
			site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/models":
					jsonReply(w, 200, catalogJSON)
				case "/saved":
					checks.Add(1)
					if tc.status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					if tc.body != "" {
						w.Header().Set("Content-Type", "text/html")
					}
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				default:
					writes.Add(1)
					created(w)
				}
			})
			dir := t.TempDir()
			localImage(t, dir, "test.png", "source")
			spec := newSpec()
			spec["images"] = []string{"test.png"}
			specPath := writeSpec(t, dir, spec)
			_, specHash, problem := readSpec(specPath)
			if problem != nil {
				t.Fatal(problem)
			}
			c, problem := newClient(site.URL, testKey, true)
			if problem != nil {
				t.Fatal(problem)
			}
			state, problem := c.lockRun(context.Background(), "v1")
			if problem != nil {
				t.Fatal(problem)
			}
			// A v1 checkpoint after the last upload but before preparing any create.
			checkpoint := record{Version: 1, Stage: "uploading", Key: taskID, SpecHash: specHash,
				Refs: []savedRef{{Hash: digest([]byte("\x89PNG\r\n\x1a\nsource")), URL: site.URL + "/saved"}}}
			problem = state.save(&checkpoint)
			state.lock.Close()
			if problem != nil {
				t.Fatal(problem)
			}
			before, err := os.ReadFile(state.path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.localAbsent {
				if err := os.Remove(filepath.Join(dir, "test.png")); err != nil {
					t.Fatal(err)
				}
			}
			v := createRun(specPath, "v1")
			v.result(t, false)
			after, err := os.ReadFile(state.path)
			// net/http may retry an idempotent GET after losing a reused connection.
			if n := checks.Load(); n < 1 || (tc.status != 0 && n != 1) {
				t.Fatalf("unexpected probe count %d", n)
			}
			if err != nil || !bytes.Equal(before, after) || writes.Load() != 0 || !strings.Contains(v.out, "image 1") {
				t.Fatalf("same checkpoint=%t, slot identified=%t, probes=%d, writes=%d, read error=%v",
					bytes.Equal(before, after), strings.Contains(v.out, "image 1"), checks.Load(), writes.Load(), err)
			}
		})
	}
}
