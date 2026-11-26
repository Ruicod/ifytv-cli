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
	"strings"
	"sync/atomic"
	"testing"
)

const continuationTestPath = "/api/v1/cli/projects/" + projectID + "/continuations"
const continuationTestNode = "Scene.007:A"

func continuationClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	site := testSite(t, handler)
	c, problem := newClient(site.URL, deviceToken, true)
	if problem != nil {
		t.Fatal(problem)
	}
	c.runScope = productionTestScope
	return c
}

func continuationReceipt(w http.ResponseWriter, node string) {
	jsonReply(w, http.StatusAccepted, fmt.Sprintf(`{"id":%q,"project_id":%q,"node_id":%q,"status":"queued"}`, taskID, projectID, node))
}

func TestCanvasContinuationReplaysExactRequestForOriginalNode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const image = "场景.png"
	localImage(t, dir, image, "scene")
	spec := newSpec()
	spec["images"] = []string{image}
	specFile := writeSpec(t, dir, spec)

	var submits, uploads, catalogs atomic.Int32
	type submission struct {
		body []byte
		key  string
	}
	var original atomic.Pointer[submission]
	c := continuationClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/models":
			catalogs.Add(1)
			jsonReply(w, http.StatusOK, catalogJSON)
		case "POST " + productionTestUploads:
			if r.Header.Get("Authorization") != "Bearer "+deviceToken {
				t.Error("private upload omitted the device bearer")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "bad upload", http.StatusBadRequest)
				return
			}
			uploads.Add(1)
			uploadReceipt(w, productionTestUploadURL(r.Host, 1), "image")
		case "POST " + continuationTestPath:
			body, err := io.ReadAll(r.Body)
			key := r.Header.Get("Idempotency-Key")
			if err != nil || !validUUID(key) || r.Header.Get("Authorization") != "Bearer "+deviceToken {
				t.Errorf("invalid pinned continuation request: %v", err)
			}
			attempt := submits.Add(1)
			if attempt == 1 {
				var envelope struct {
					Spec     videoSpec `json:"spec"`
					Names    []string  `json:"reference_names"`
					Revision uint64    `json:"expected_revision"`
					Target   string    `json:"target_node_id"`
				}
				if err := json.Unmarshal(body, &envelope); err != nil || envelope.Target != continuationTestNode ||
					envelope.Revision != 17 || !slices.Equal(envelope.Names, []string{image}) ||
					!slices.Equal(envelope.Spec.Images, []string{productionTestUploadURL(r.Host, 1)}) {
					t.Errorf("incorrect continuation envelope: %+v (%v)", envelope, err)
				}
				original.Store(&submission{body: body, key: key})
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			if first := original.Load(); first == nil || first.key != key || !bytes.Equal(first.body, body) {
				t.Error("continuation replay changed the original request")
			}
			if attempt == 2 {
				continuationReceipt(w, "Different-Node")
				return
			}
			continuationReceipt(w, continuationTestNode)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	o, problem := parseArgs([]string{"video", "create", "--project", projectID, "--node", continuationTestNode,
		"--revision", "17", "--spec", specFile, "--run", "same card", "--authorization-id", productionTestConsent})
	if problem != nil {
		t.Fatal(problem)
	}
	result, problem := c.create(ctx, o)
	if problem == nil || problem.Code != "network_error" || result.NodeID != continuationTestNode || result.ProjectID != projectID {
		t.Fatalf("lost response did not preserve the canvas destination: %+v %v", result, problem)
	}

	if err := os.Remove(filepath.Join(dir, image)); err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.revision++
	if result, problem := c.create(ctx, changed); problem == nil || problem.Code != "run_project_conflict" || result.NodeID != continuationTestNode {
		t.Fatalf("the same run accepted a new revision: %+v %v", result, problem)
	}
	changed = o
	changed.node = ""
	if _, problem := c.create(ctx, changed); problem == nil || problem.Code != "run_project_conflict" {
		t.Fatalf("the same revision accepted removal of the saved target: %v", problem)
	}

	bare := options{command: "video create", run: o.run, spec: specFile}
	if _, problem := c.create(ctx, bare); problem == nil || problem.Code != "device_login_required" {
		t.Fatalf("un-pinned replay was not blocked: %v", problem)
	}
	bare.authorizationID = productionTestConsent
	if _, problem := c.create(ctx, bare); problem == nil || problem.Code != "invalid_response" {
		t.Fatalf("the run accepted a different node receipt: %v", problem)
	}
	for _, retry := range []options{bare, o} {
		result, problem = c.create(ctx, retry)
		if problem != nil || result.NodeID != continuationTestNode || result.TaskID != taskID || result.ProjectID != projectID {
			t.Fatalf("replay did not confirm the original card: %+v %v", result, problem)
		}
	}
	if submits.Load() != 3 || uploads.Load() != 1 || catalogs.Load() != 1 {
		t.Fatalf("expected 3 posts, 1 private upload and 1 catalog, got %d posts, %d uploads and %d catalog requests",
			submits.Load(), uploads.Load(), catalogs.Load())
	}
}

func TestCanvasContinuationConflictKeepsPreparedRun(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
	}{
		{"stale revision", "canvas_revision_changed", http.StatusConflict},
		{"unknown conflict", "idempotency_conflict", http.StatusConflict},
		{"missing endpoint", "not_found", http.StatusNotFound},
		{"forbidden project", "cli_grant_required", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			o := options{command: "video create", project: projectID, node: continuationTestNode, revision: 31,
				run: "guarded conflict", spec: writeSpec(t, t.TempDir(), newSpec()), authorizationID: productionTestConsent}
			var submits atomic.Int32
			c := continuationClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/models":
					jsonReply(w, http.StatusOK, catalogJSON)
				case "POST " + continuationTestPath:
					submits.Add(1)
					jsonReply(w, tc.status, fmt.Sprintf(`{"error":{"code":%q}}`, tc.code))
				default:
					t.Errorf("unexpected fallback request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			for range 2 {
				result, problem := c.create(ctx, o)
				if problem == nil || problem.Code != tc.code || problem.HTTPStatus != tc.status ||
					result.ProjectID != projectID || result.NodeID != continuationTestNode || result.TaskID != "" {
					t.Fatalf("unexpected guarded failure: %+v %v", result, problem)
				}
				if tc.status == http.StatusForbidden && result.AuthorizationURL != c.base.String()+"/cli?project="+projectID {
					t.Fatalf("missing scoped re-authorization link: %+v", result)
				}
			}
			if submits.Load() != 2 {
				t.Fatalf("submitted %d requests to the guarded route", submits.Load())
			}
			file, problem := c.lockRun(ctx, o.run)
			if problem != nil {
				t.Fatal(problem)
			}
			defer file.unlock()
			record, problem := file.load()
			if problem != nil || record == nil || record.Version != 5 || record.Stage != "prepared" || record.Task != nil || !validUUID(record.Key) {
				t.Fatalf("faulted continuation lost its prepared request: %+v %v", record, problem)
			}
		})
	}
}

func TestCanvasContinuationRecoversPrivateReferencesBeforePreparing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply int
		code  string
	}{
		{"reuse a live URL", http.StatusPartialContent, ""},
		{"replace an expired URL", http.StatusGone, ""},
		{"reject a redirect", http.StatusFound, "redirect_rejected"},
		{"keep the reference on access denial", http.StatusForbidden, "cli_grant_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			localImage(t, dir, "first.png", "first")
			localImage(t, dir, "second.png", "second")
			spec := newSpec()
			spec["images"] = []string{"first.png", "second.png"}
			o := options{command: "video create", project: projectID, authorizationID: productionTestConsent,
				revision: 425, run: "missing card", spec: writeSpec(t, dir, spec)}
			var first, second, probes, submits atomic.Int32
			c := continuationClient(t, func(w http.ResponseWriter, r *http.Request) {
				const redirectPath = "/api/v1/tasks"
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/models":
					jsonReply(w, http.StatusOK, catalogJSON)
				case "POST " + productionTestUploads:
					if r.Header.Get("Authorization") != "Bearer "+deviceToken {
						t.Error("private upload omitted the device bearer")
					}
					f, part, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						http.Error(w, "bad upload", http.StatusBadRequest)
						return
					}
					f.Close()
					switch part.Filename {
					case "first.png":
						n := first.Add(1)
						uploadReceipt(w, productionTestUploadURL(r.Host, n), "image")
					case "second.png":
						if second.Add(1) == 1 {
							jsonReply(w, http.StatusForbidden, `{"error":{"code":"cli_grant_required"}}`)
							return
						}
						uploadReceipt(w, productionTestUploadURL(r.Host, 3), "image")
					default:
						t.Errorf("unexpected staged file %q", part.Filename)
						http.NotFound(w, r)
					}
				case "GET " + strings.TrimPrefix(productionTestUploadURL(r.Host, 1), "http://"+r.Host):
					probes.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+deviceToken || r.Header.Get("Range") != "bytes=0-0" {
						t.Error("recovery did not use an authenticated, bounded read")
					}
					switch tc.reply {
					case http.StatusPartialContent:
						w.Header().Set("Content-Type", "image/png")
						w.WriteHeader(http.StatusPartialContent)
						io.WriteString(w, "\x89")
					case http.StatusFound:
						http.Redirect(w, r, redirectPath, http.StatusFound)
					default:
						jsonReply(w, tc.reply, `{"error":{"code":"cli_grant_required"}}`)
					}
				case "POST " + continuationTestPath:
					submits.Add(1)
					var request struct {
						Spec     videoSpec `json:"spec"`
						Revision uint64    `json:"expected_revision"`
						Target   *string   `json:"target_node_id"`
					}
					wantImages := []string{productionTestUploadURL(r.Host, first.Load()), productionTestUploadURL(r.Host, 3)}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
						request.Revision != o.revision || request.Target != nil || !slices.Equal(request.Spec.Images, wantImages) {
						t.Errorf("incorrect resolved append request: %+v (%v)", request, err)
					}
					continuationReceipt(w, continuationTestNode)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			if _, problem := c.create(ctx, o); problem == nil || problem.Code != "cli_grant_required" || submits.Load() != 0 {
				t.Fatalf("first run did not checkpoint its partial upload: %v", problem)
			}
			if tc.reply == http.StatusPartialContent {
				if err := os.Remove(filepath.Join(dir, "first.png")); err != nil {
					t.Fatal(err)
				}
			}
			result, problem := c.create(ctx, o)
			if tc.code != "" {
				if problem == nil || problem.Code != tc.code || probes.Load() != 1 ||
					submits.Load() != 0 || first.Load() != 1 || second.Load() != 1 {
					t.Fatalf("unsafe recovery after private URL rejection: %+v %v", result, problem)
				}
				return
			}
			wantFirst := int32(1)
			if tc.reply == http.StatusGone {
				wantFirst++
			}
			if problem != nil || result.NodeID != continuationTestNode || result.TaskID != taskID ||
				probes.Load() != 1 || submits.Load() != 1 || first.Load() != wantFirst || second.Load() != 2 {
				t.Fatalf("private recovery returned %+v with %v, got %d/%d uploads, %d probes and %d posts",
					result, problem, first.Load(), second.Load(), probes.Load(), submits.Load())
			}
		})
	}
}

func TestCanvasContinuationRejectsRemoteReferences(t *testing.T) {
	spec := newSpec()
	spec["audio"] = []string{"https://example.invalid/audio.mp3"}
	o := options{command: "video create", project: projectID, authorizationID: productionTestConsent,
		revision: 425, run: "remote reference", spec: writeSpec(t, t.TempDir(), spec)}
	c := continuationClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	if _, problem := c.create(context.Background(), o); problem == nil || problem.Code != "invalid_reference" {
		t.Fatalf("the guarded path accepted an external reference: %v", problem)
	}
}
