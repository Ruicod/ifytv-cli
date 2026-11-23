package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// Re-exec the real entry point so process termination closes the kernel lock
// without any Go cleanup and the second invocation starts with empty memory.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("IFYTV_TEST_PROCESS") != "child" {
		t.Skip("subprocess entry point")
	}
	i := slices.Index(os.Args, "--")
	if i < 0 {
		t.Fatal("missing CLI argument separator")
	}
	os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
	main()
}

func TestKilledCreateProcessReplaysDurableRequest(t *testing.T) {
	const id = "d6b116f8-62e4-4b8d-9bff-f9b9326e72f9"
	const key = "private-synthetic-subprocess-key"
	type attempt struct {
		key  string
		body []byte
	}
	received := make(chan attempt, 2)
	unblock := make(chan struct{})
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/models":
			io.WriteString(w, `{"models":[{"id":"synthetic","modes":["text_to_video"],"ratios":["16:9"],"duration_kind":"fixed","fixed_duration":4,"max_images":0}]}`)
		case "/api/v1/tasks":
			b, err := io.ReadAll(r.Body)
			if err != nil || r.Header.Get("Authorization") != "Bearer "+key {
				t.Error("invalid create")
			}
			n := count.Add(1)
			received <- attempt{r.Header.Get("Idempotency-Key"), b}
			if n == 1 {
				select {
				case <-r.Context().Done():
					return
				case <-unblock:
					return
				}
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"`+id+`","status":"queued"}`)
		default:
			t.Error("unexpected request")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(unblock)
	state := t.TempDir()
	t.Setenv("IFYTV_STATE_DIR", state)
	t.Setenv("IFYTV_BASE_URL", server.URL)
	t.Setenv("IFYTV_API_KEY", key)
	t.Setenv("IFYTV_TEST_PROCESS", "child")
	spec := filepath.Join(t.TempDir(), "shot.json")
	if err := os.WriteFile(spec, []byte(`{"model":"synthetic","mode":"text_to_video","prompt":"猫 & <dog>","duration":4,"ratio":"16:9"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := func() *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIProcess$", "--", "video", "create", "--spec", spec, "--run", "crash", "--json")
	}
	first := command()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if first.ProcessState == nil {
			first.Process.Kill()
			first.Wait()
		}
	}()
	var sent attempt
	select {
	case sent = <-received:
	case <-ctx.Done():
		t.Fatal("no create from first process")
	}
	files, err := filepath.Glob(filepath.Join(state, "runs", "*", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("missing pre-create checkpoint", err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil || bytes.Contains(b, []byte(key)) {
		t.Fatal("invalid state read or exposed key")
	}
	var saved struct {
		Stage   string
		Key     string `json:"idempotency_key"`
		Payload json.RawMessage
	}
	if err := json.Unmarshal(b, &saved); err != nil || saved.Stage != "prepared" || saved.Key == "" || saved.Key != sent.key || !bytes.Equal(saved.Payload, sent.body) {
		t.Fatal("durable request differs from bytes received by the server", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(files[0]); err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("state file is not private", err)
		}
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err == nil {
		t.Fatal("first process was expected to terminate before receiving a reply")
	}
	data, err := command().Output()
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err != nil || json.Unmarshal(data, &result) != nil || result.TaskID != id {
		t.Fatal("fresh process did not recover the original task", err)
	}
	select {
	case again := <-received:
		if again.key != sent.key || !bytes.Equal(again.body, sent.body) {
			t.Fatal("fresh process changed the idempotency key or exact payload")
		}
	default:
		t.Fatal("fresh process did not replay the saved request")
	}
	if _, err := command().Output(); err != nil || count.Load() != 2 {
		t.Fatal("a confirmed run was submitted again", err)
	}
}
