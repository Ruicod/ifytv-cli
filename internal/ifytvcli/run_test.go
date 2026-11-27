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
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func localImage(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("\x89PNG\r\n\x1a\n"+value), 0600); err != nil {
		t.Fatal(err)
	}
}

func receiveUpload(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testKey {
		t.Error("unauthenticated upload or wrong method")
	}
	reader, err := r.MultipartReader()
	if err != nil {
		t.Error(err)
		return "", ""
	}
	p, err := reader.NextPart()
	if err != nil {
		t.Error(err)
		return "", ""
	}
	name := p.FileName()
	data, err := io.ReadAll(p)
	if err != nil || p.FormName() != "file" || len(data) == 0 {
		t.Error("empty or invalid upload", err)
	}
	return name, p.Header.Get("Content-Type")
}

func uploadReply(w http.ResponseWriter, name, kind string) {
	uploadReceipt(w, "https://media.example/"+name, kind)
}

func uploadReceipt(w http.ResponseWriter, url, kind string) {
	jsonReply(w, 201, fmt.Sprintf(`{"url":%q,"media_type":%q}`, url, kind))
}

func TestOrderedReferencesDespiteReverseUploadCompletion(t *testing.T) {
	dir := t.TempDir()
	spec := newSpec()
	images := []string{"掌柜 A.png", "顾客.png", "学徒.png", "店铺.png", "道具.png"}
	audio := []string{"掌柜.mp3", "顾客.mp3", "学徒.mp3"}
	names := append(slices.Clone(images), audio...)
	for _, name := range images {
		localImage(t, dir, name, name)
	}
	for _, name := range audio {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ID3"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	spec["images"], spec["audio"] = images, audio
	arrived := make(chan int, 8)
	finished := make(chan int, 8)
	gates := make([]chan struct{}, len(names))
	for i := range gates {
		gates[i] = make(chan struct{})
	}
	var completed atomic.Int32
	var creates atomic.Int32
	slots := make(chan struct{}, 4) // Gateway's default UPLOAD_CONCURRENCY.
	var busy atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				busy.Add(1)
				jsonReply(w, 429, `{"error":{"code":"upload_busy"}}`)
				return
			}
			name, typ := receiveUpload(t, r)
			idx := slices.Index(names, name)
			if idx < 0 {
				t.Errorf("unexpected upload %q", name)
				http.Error(w, "bad name", 400)
				return
			}
			kind := "image"
			if idx >= len(images) {
				kind = "audio"
			}
			if !strings.HasPrefix(typ, kind+"/") {
				t.Errorf("wrong media type %s", typ)
			}
			arrived <- idx
			select {
			case <-gates[idx]:
			case <-r.Context().Done():
				return
			}
			completed.Add(1)
			uploadReply(w, fmt.Sprintf("%d", idx), kind)
			finished <- idx
		case "/api/v1/tasks":
			creates.Add(1)
			if completed.Load() != 8 || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+testKey || r.Header.Get("Idempotency-Key") == "" {
				t.Error("incomplete or unauthenticated create")
			}
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Error(err)
			}
			for field, want := range map[string][]any{"images": {"https://media.example/0", "https://media.example/1", "https://media.example/2", "https://media.example/3", "https://media.example/4"}, "audio": {"https://media.example/5", "https://media.example/6", "https://media.example/7"}} {
				if !reflect.DeepEqual(got[field], want) {
					t.Errorf("%s = %v", field, got[field])
				}
			}
			if got["prompt"] != spec["prompt"] || got["mode"] != "omni" {
				t.Error("changed spec fields")
			}
			created(w)
		default:
			http.NotFound(w, r)
		}
	})
	file := writeSpec(t, dir, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan invocation, 1)
	go func() {
		done <- invoke(ctx, "video", "create", "--no-canvas", "--spec", file, "--run", "镜头-7", "--json")
	}()
	for range 2 {
		batch := make([]int, 0, 4)
		for range 4 {
			select {
			case i := <-arrived:
				batch = append(batch, i)
			case <-ctx.Done():
				t.Fatal("expected a batch of concurrent uploads")
			case v := <-done:
				t.Fatalf("create exited before uploads: %s", v.out)
			}
		}
		slices.Sort(batch)
		slices.Reverse(batch)
		for _, i := range batch {
			close(gates[i])
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	o := (<-done).result(t, true)
	if creates.Load() != 1 || o["task_id"] != taskID || busy.Load() != 0 {
		t.Fatal(o)
	}
}

func TestSharedFourSlotGatewayHonorsRetryAfter(t *testing.T) {
	dir := t.TempDir()
	names := []string{"one.png", "two.png", "three.png", "four.png"}
	for _, name := range names {
		localImage(t, dir, name, name)
	}
	spec := newSpec()
	spec["images"] = names
	slots := make(chan struct{}, 4)
	slots <- struct{}{} // A different client owns one slot at startup.
	denied := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var rejected string
	var when time.Time
	var retried atomic.Int32
	var successes atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			name, _ := receiveUpload(t, r)
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				select {
				case <-denied:
				case <-r.Context().Done():
					return
				}
				mu.Lock()
				if rejected == name {
					retried.Add(1)
					if time.Since(when) < time.Second {
						t.Error("retried before Retry-After")
					}
				}
				mu.Unlock()
				successes.Add(1)
				uploadReply(w, name, "image")
			default:
				once.Do(func() { mu.Lock(); rejected, when = name, time.Now(); mu.Unlock(); <-slots; close(denied) })
				w.Header().Set("Retry-After", "1")
				jsonReply(w, 429, `{"error":{"code":"upload_busy"}}`)
			}
		case "/api/v1/tasks":
			if successes.Load() != 4 {
				t.Error("created before all uploads succeeded")
			}
			created(w)
		default:
			t.Error(r.URL.Path)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := invoke(ctx, "video", "create", "--no-canvas", "--spec", writeSpec(t, dir, spec), "--run", "share", "--json")
	v.result(t, true)
	if retried.Load() != 1 {
		t.Fatal("expected one throttled slot to retry", retried.Load())
	}
}

func TestUploadBackoffIsBoundedAndRetryUsesSuccessfulCheckpoint(t *testing.T) {
	dir := t.TempDir()
	spec := newSpec()
	spec["images"] = []string{"one.png", "two.png"}
	for _, name := range spec["images"].([]string) {
		localImage(t, dir, name, name)
	}
	var first, second, creates atomic.Int32
	var recovered atomic.Bool
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			name, _ := receiveUpload(t, r)
			if name == "one.png" {
				first.Add(1)
			} else {
				second.Add(1)
				if !recovered.Load() {
					jsonReply(w, 429, `{"error":{"code":"upload_busy"}}`)
					return
				}
			}
			uploadReceipt(w, "http://"+r.Host+"/uploads/"+name, "image")
		case "/uploads/one.png":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "\x89PNG\r\n\x1a\none")
		case "/api/v1/tasks":
			creates.Add(1)
			created(w)
		default:
			t.Error(r.URL.Path)
		}
	})
	file := writeSpec(t, dir, spec)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v := invoke(ctx, "video", "create", "--no-canvas", "--spec", file, "--run", "throttled", "--json")
	o := v.result(t, false)
	attempts := second.Load()
	if o["error"].(map[string]any)["code"] != "upload_busy" || !strings.Contains(v.out, "image 2") || creates.Load() != 0 || attempts < 2 || attempts > 4 {
		t.Fatal(o, creates.Load(), attempts)
	}
	recovered.Store(true)
	createRun(file, "throttled").result(t, true)
	if first.Load() != 1 || second.Load() != attempts+1 || creates.Load() != 1 {
		t.Fatal("successful uploads were replayed")
	}
}

func TestFailedUploadCheckpointsSuccessfulSlots(t *testing.T) {
	dir := t.TempDir()
	spec := newSpec()
	spec["audio"] = []string{"one.mp3", "two.mp3", "three.mp3"}
	for _, name := range spec["audio"].([]string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ID3"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	calls := map[string]int{}
	var creates atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			name, _ := receiveUpload(t, r)
			mu.Lock()
			calls[name]++
			attempt := calls[name]
			mu.Unlock()
			if name == "two.mp3" && attempt == 1 {
				jsonReply(w, 503, `{"error":{"code":"upload_storage_failed"}}`)
				return
			}
			uploadReceipt(w, "http://"+r.Host+"/uploads/"+name, "audio")
		case "/uploads/one.mp3", "/uploads/three.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			io.WriteString(w, "I")
		case "/api/v1/tasks":
			creates.Add(1)
			created(w)
		default:
			t.Error(r.URL.Path)
		}
	})
	file := writeSpec(t, dir, spec)
	v := createRun(file, "retry")
	v.result(t, false)
	if creates.Load() != 0 || !strings.Contains(strings.ToLower(v.out), "audio 2") {
		t.Fatal("missing numbered failure or premature create", v.out)
	}
	createRun(file, "retry").result(t, true)
	mu.Lock()
	defer mu.Unlock()
	if creates.Load() != 1 || !reflect.DeepEqual(calls, map[string]int{"one.mp3": 1, "two.mp3": 2, "three.mp3": 1}) {
		t.Fatal(calls, creates.Load())
	}
}

func TestLostCreateReplyReusesCheckpointAndRejectsChangedInput(t *testing.T) {
	dir := t.TempDir()
	spec := newSpec()
	localImage(t, dir, "frame.png", "initial")
	spec["images"] = []string{"frame.png"}
	file := writeSpec(t, dir, spec)
	var uploads, creates, models atomic.Int32
	var mu sync.Mutex
	var key string
	var exact []byte
	site := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			models.Add(1)
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			receiveUpload(t, r)
			uploads.Add(1)
			uploadReply(w, "frame.png", "image")
		case "/api/v1/tasks":
			data, _ := io.ReadAll(r.Body)
			mu.Lock()
			defer mu.Unlock()
			if creates.Add(1) == 1 {
				key, exact = r.Header.Get("Idempotency-Key"), data
				if key == "" {
					t.Error("missing idempotency key")
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
			if key != r.Header.Get("Idempotency-Key") || !bytes.Equal(exact, data) {
				t.Error("changed replay key or payload")
			}
			created(w)
		case "/api/v1/tasks/" + taskID:
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"processing"}`, taskID))
		default:
			t.Error(r.URL.Path)
		}
	})
	createRun(file, "lost").result(t, false)
	if creates.Load() != 1 {
		t.Fatal("expected one ambiguous create")
	}
	localImage(t, dir, "frame.png", "changed")
	v := createRun(file, "lost")
	v.result(t, false)
	if creates.Load() != 1 || !strings.Contains(v.out, "run") {
		t.Fatal("changed file was accepted")
	}
	localImage(t, dir, "frame.png", "initial")
	o := createRun(file, "lost").result(t, true)
	if o["task_id"] != taskID || uploads.Load() != 1 || creates.Load() != 2 || models.Load() != 1 {
		t.Fatal(o, uploads.Load(), creates.Load(), models.Load())
	}
	createRun(file, "lost").result(t, true)
	if creates.Load() != 2 {
		t.Fatal("created a confirmed task again")
	}
	spec["prompt"] = "different"
	writeSpec(t, dir, spec)
	createRun(file, "lost").result(t, false)
	if creates.Load() != 2 {
		t.Fatal("changed spec was submitted")
	}
	invoke(context.Background(), "video", "status", "--run", "lost", "--json").result(t, true)
	t.Setenv("IFYTV_API_KEY", "second-credential")
	invoke(context.Background(), "video", "status", "--run", "lost", "--json").result(t, false)
	t.Setenv("IFYTV_API_KEY", testKey)
	t.Setenv("IFYTV_BASE_URL", site.URL+"/other-site")
	invoke(context.Background(), "video", "status", "--run", "lost", "--json").result(t, false)
}

func TestPreflightRejectsEntireSpecBeforeAnyUploadOrCreate(t *testing.T) {
	dir := t.TempDir()
	localImage(t, dir, "ok.png", "valid")
	var writes atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/models" {
			jsonReply(w, 200, catalogJSON)
			return
		}
		writes.Add(1)
		http.Error(w, "unexpected", 500)
	})
	for i, change := range []map[string]any{
		{"images": []string{"ok.png", "missing.png"}}, {"audio": []string{"ok.png"}}, {"audio": []string{"file://host/foo"}},
		{"model": "absent"}, {"duration": 9}, {"mode": "unknown"}, {"ratio": "4:3"}, {"resolution": "4K"},
		{"mode": "first_last_frame", "images": []string{"ok.png", "ok.png", "ok.png"}}, {"mode": "text_to_video", "images": []string{"ok.png"}},
		{"mode": "text_to_video", "audio": []string{"https://cdn.example/a.mp3"}}, {"mode": "omni", "audio": []string{"a", "b", "c", "d"}},
		{"billing_hold_id": "forbidden"}, {"images": []string{"https://name:password@cdn.example/media"}},
	} {
		spec := newSpec()
		for k, v := range change {
			spec[k] = v
		}
		file := writeSpec(t, dir, spec)
		createRun(file, fmt.Sprintf("bad-%d", i)).result(t, false)
	}
	if writes.Load() != 0 {
		t.Fatal("preflight performed a write", writes.Load())
	}
}

func TestVideoFieldsAndMixedRemoteReferences(t *testing.T) {
	dir := t.TempDir()
	localImage(t, dir, "frame.png", "frame")
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), mp4Fixture, 0600); err != nil {
		t.Fatal(err)
	}
	// User-supplied remote references are opaque, including signed URLs.
	const remote = "https://media.example/known.mp4?signature=private"
	spec := newSpec()
	spec["images"] = []string{"https://media.example/first.png", "frame.png"}
	spec["videos"] = []string{remote}
	spec["face_check"] = false
	spec["generate_audio"] = false
	required := strings.Replace(catalogJSON, `"video_supported":true`, `"video_required":true,"video_supported":true`, 1)
	var uploads, creates atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			if r.Header.Get("Authorization") != "" {
				t.Error("authenticated catalog preflight")
			}
			jsonReply(w, 200, required)
		case "/api/v1/uploads":
			name, typ := receiveUpload(t, r)
			uploads.Add(1)
			uploadReply(w, name, strings.SplitN(typ, "/", 2)[0])
		case "/api/v1/tasks":
			creates.Add(1)
			var body struct {
				Images, Videos []string
				VideoURL       string `json:"video_url"`
				Face           *bool  `json:"face_check"`
				Generate       *bool  `json:"generate_audio"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !slices.Equal(body.Images, []string{"https://media.example/first.png", "https://media.example/frame.png"}) ||
				!slices.Equal(body.Videos, []string{remote}) || body.VideoURL != "https://media.example/clip.mp4" || body.Face == nil || *body.Face || body.Generate == nil || *body.Generate {
				t.Error("gateway video fields, slot order or explicit false lost")
			}
			created(w)
		default:
			t.Error(r.URL.Path)
		}
	})
	file := writeSpec(t, dir, spec)
	o := createRun(file, "edit").result(t, false)
	if o["error"].(map[string]any)["code"] != "video_url_required" || uploads.Load() != 0 || creates.Load() != 0 {
		t.Fatal(o)
	}
	spec["video_url"] = "clip.mp4"
	writeSpec(t, dir, spec)
	v := createRun(file, "edit")
	v.result(t, true)
	if strings.Contains(v.out+v.diagnostics, "signature=") || uploads.Load() != 2 || creates.Load() != 1 {
		t.Fatal("unexpected reference disclosure or request count")
	}
}

func TestLongRetryAfterReturnsWithoutRetryingEarly(t *testing.T) {
	dir := t.TempDir()
	localImage(t, dir, "ref.png", "image")
	spec := newSpec()
	spec["images"] = []string{"ref.png"}
	var attempts atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/uploads":
			attempts.Add(1)
			w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
			jsonReply(w, 429, `{"error":{"code":"upload_busy"}}`)
		default:
			t.Error("unexpected create")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o := invoke(ctx, "video", "create", "--no-canvas", "--spec", writeSpec(t, dir, spec), "--run", "busy", "--json").result(t, false)
	if attempts.Load() != 1 || o["error"].(map[string]any)["code"] != "upload_busy" {
		t.Fatal(o, attempts.Load())
	}
}

func TestConcurrentCreatesShareOneRun(t *testing.T) {
	file := writeSpec(t, t.TempDir(), newSpec())
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/models" {
			jsonReply(w, 200, catalogJSON)
			return
		}
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		created(w)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make(chan invocation, 2)
	start := func() {
		out <- invoke(ctx, "video", "create", "--no-canvas", "--run", "same", "--spec", file, "--json")
	}
	go start()
	select {
	case <-entered:
	case <-ctx.Done():
		close(release)
		t.Fatal("first create did not arrive")
	}
	go start()
	close(release)
	for range 2 {
		if (<-out).result(t, true)["task_id"] != taskID {
			t.Fatal("wrong task")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent duplicate create")
	}
}
