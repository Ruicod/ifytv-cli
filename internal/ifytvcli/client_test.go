package ifytvcli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthenticatedSitesRequireTLSOutsideLoopback(t *testing.T) {
	for _, site := range []string{"http://example.invalid", "http://192.0.2.1:8080", "http://localhost.example.invalid", "http://[::]", "http://0.0.0.0", "http://127.0.0.1.example.invalid", "http://name:password@localhost", "http://localhost:8080/?token=x"} {
		if _, problem := newClient(site, testKey, true); problem == nil {
			t.Errorf("accepted insecure authenticated site %s", site)
		}
	}
	for _, site := range []string{"http://localhost:8080", "http://LOCALHOST", "http://127.0.0.2:8080", "http://[::1]:8080", "https://example.invalid", ""} {
		c, problem := newClient(site, testKey, true)
		if problem != nil || (site == "" && c.base.String() != "https://video.fastaitoken.com") {
			t.Error("rejected trusted site", site, problem)
		}
	}
	var calls atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("models sent Authorization with a configured key")
		}
		jsonReply(w, 200, catalogJSON)
	})
	invoke(context.Background(), "models", "--json").result(t, true)
	if calls.Load() != 1 {
		t.Error("expected one catalog request")
	}
	if _, problem := newClient("http://example.invalid", testKey, false); problem != nil {
		t.Fatal("public HTTP catalog rejected")
	}
}

func TestDownloadAcrossOriginsStripsHeadersAndNeverOverwrites(t *testing.T) {
	var dstCalls atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dstCalls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" {
			t.Error("cross-origin request carried credentials or referrer")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(mp4Fixture)))
		w.Write(mp4Fixture)
	}))
	defer dst.Close()
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks/"+taskID+"/download" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("incorrect download request")
		}
		http.Redirect(w, r, dst.URL+"/signed?token=private", 302)
	})
	file := filepath.Join(t.TempDir(), "成片.mp4")
	args := []string{"video", "download", "--task", taskID, "--out", file, "--json"}
	o := invoke(context.Background(), args...).result(t, true)
	data, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(data, mp4Fixture) || o["path"] != file {
		t.Fatal(o, err)
	}
	invoke(context.Background(), args...).result(t, false)
	if dstCalls.Load() != 1 {
		t.Fatal("download retried over existing destination")
	}
}

func TestInterruptedPartialAndNonVideoDownloadsLeaveNoFile(t *testing.T) {
	for _, variant := range []string{"short", "range", "html", "masked html", "empty", "chunked"} {
		t.Run(variant, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				switch variant {
				case "short":
					w.Header().Set("Content-Length", strconv.Itoa(len(mp4Fixture)+1))
					w.Write(mp4Fixture)
				case "range":
					w.Header().Set("Content-Range", "bytes 0-35/100")
					w.WriteHeader(206)
					w.Write(mp4Fixture)
				case "html":
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte("<html>login</html>"))
				case "masked html":
					w.Write([]byte("<html>login</html>"))
				case "empty":
					w.Header().Set("Content-Length", "0")
				case "chunked":
					w.(http.Flusher).Flush()
					w.Write(mp4Fixture)
				}
			})
			dir := t.TempDir()
			invoke(context.Background(), "video", "download", "--task", taskID, "--out", filepath.Join(dir, "x.mp4"), "--json").result(t, false)
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("download left files", files, err)
			}
		})
	}
}

func TestDownloadPreservesDestinationCreatedDuringTransfer(t *testing.T) {
	start := make(chan struct{})
	finish := make(chan struct{})
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(mp4Fixture)))
		w.(http.Flusher).Flush()
		close(start)
		<-finish
		w.Write(mp4Fixture)
	})
	dir := t.TempDir()
	file := filepath.Join(dir, "existing.mp4")
	done := make(chan invocation, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- invoke(ctx, "video", "download", "--task", taskID, "--out", file, "--json") }()
	select {
	case <-start:
	case <-ctx.Done():
		close(finish)
		t.Fatal("download did not start")
	}
	err := os.WriteFile(file, []byte("original"), 0600)
	close(finish)
	if err != nil {
		t.Fatal(err)
	}
	(<-done).result(t, false)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "original" {
		t.Fatal("destination overwritten", err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("temporary file left behind", files)
	}
}

func TestRedirectedWritesNeverSendBodiesToAnotherOrigin(t *testing.T) {
	var hits atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		jsonReply(w, 200, `{}`)
	}))
	defer dst.Close()
	dir := t.TempDir()
	spec := newSpec()
	localImage(t, dir, "private.png", "private reference")
	for _, action := range []string{"create", "upload", "import"} {
		t.Run(action, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/models" {
					jsonReply(w, 200, catalogJSON)
					return
				}
				w.Header().Set("Location", dst.URL+"/?secret=opaque")
				w.WriteHeader(http.StatusTemporaryRedirect)
			})
			var res invocation
			if action == "import" {
				res = invoke(context.Background(), "canvas", "import", "--task", taskID, "--project", projectID, "--json")
			} else {
				if action == "upload" {
					spec["images"] = []string{"private.png"}
				}
				res = createRun(writeSpec(t, dir, spec), "redirect")
			}
			res.result(t, false)
			if strings.Contains(res.out+res.diagnostics, "secret=opaque") {
				t.Fatal("signed URL exposed")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("untrusted origin received %d requests", hits.Load())
	}
}
