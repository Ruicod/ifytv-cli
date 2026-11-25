package ifytvcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	canvasTeamOne = "20e18f3e-747f-4ad6-9daa-fd671070ffad"
	canvasTeamTwo = "51caed14-a829-4d16-99c4-b7fc36bdd541"
)

func canvasHTTPClient(t *testing.T, key string, handler http.HandlerFunc) *client {
	t.Helper()
	site := httptest.NewServer(handler)
	t.Cleanup(site.Close)
	c, problem := newClient(site.URL, key, true)
	if problem != nil {
		t.Fatal(problem)
	}
	return c
}

func TestCanvasWorkspacesOnlyReturnsWritableTeamsAndEligibleDefault(t *testing.T) {
	for _, tc := range []struct{ name, defaultID, wantDefault string }{
		{"writable team", strings.ToUpper(canvasTeamTwo), canvasTeamTwo},
		{"read-only team", assetID, ""},
		{"personal", taskID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/api/v1/cli/workspaces" ||
					r.Header.Get("Authorization") != "Bearer "+deviceToken || r.Header.Get("Accept") != "application/json" ||
					r.Header.Get("Idempotency-Key") != "" || r.ContentLength != 0 {
					t.Error("incorrect workspace list request")
				}
				jsonReply(w, 200, fmt.Sprintf(`{"workspaces":[
					{"id":%q,"name":"Personal","kind":"personal","can_create_project":true},
					{"id":%q,"name":"View only","kind":"team","can_create_project":false},
					{"id":%q,"name":"剪辑组","kind":"team","can_create_project":true},
					{"id":%q,"name":"Studio","kind":"team","can_create_project":true}
				],"default_workspace_id":%q}`, taskID, assetID, strings.ToUpper(canvasTeamOne), canvasTeamTwo, tc.defaultID))
			})
			got, defaultID, problem := c.canvasWorkspaces(context.Background())
			want := []canvasWorkspace{{canvasTeamOne, "剪辑组", "team", true}, {canvasTeamTwo, "Studio", "team", true}}
			if problem != nil || !reflect.DeepEqual(got, want) || defaultID != tc.wantDefault {
				t.Fatalf("workspaces %v, default %q, error %v", got, defaultID, problem)
			}
		})
	}
}

func TestCanvasWorkspaceSelection(t *testing.T) {
	team := canvasWorkspace{ID: canvasTeamOne, Name: "Post", Kind: "team", CanCreateProject: true}
	second := canvasWorkspace{ID: canvasTeamTwo, Name: "Editorial", Kind: "team", CanCreateProject: true}
	readOnly := canvasWorkspace{ID: taskID, Name: "Viewers", Kind: "team"}
	personal := canvasWorkspace{ID: assetID, Name: "Personal", Kind: "personal", CanCreateProject: true}
	mixed := []canvasWorkspace{personal, team, readOnly, second}
	for _, tc := range []struct {
		name                         string
		rows                         []canvasWorkspace
		defaultID, requested, wantID string
		wantError                    string
		wantOptions                  []canvasWorkspace
	}{
		{"explicit beats default", mixed, canvasTeamOne, strings.ToUpper(canvasTeamTwo), canvasTeamTwo, "", nil},
		{"explicit read-only never falls back", mixed, canvasTeamTwo, readOnly.ID, "", "workspace_not_writable", nil},
		{"explicit personal never falls back", mixed, canvasTeamOne, personal.ID, "", "workspace_not_writable", nil},
		{"explicit unknown never falls back", []canvasWorkspace{team}, "", projectID, "", "workspace_not_writable", nil},
		{"server default", mixed, canvasTeamTwo, "", canvasTeamTwo, "", nil},
		{"only writable team", []canvasWorkspace{personal, team, readOnly}, "", "", canvasTeamOne, "", nil},
		{"ineligible default with sole team", []canvasWorkspace{personal, team}, personal.ID, "", canvasTeamOne, "", nil},
		{"choose among writable teams", mixed, "", "", "", "workspace_selection_required", []canvasWorkspace{team, second}},
		{"ineligible default cannot decide", mixed, readOnly.ID, "", "", "workspace_selection_required", []canvasWorkspace{team, second}},
		{"no writable team", []canvasWorkspace{personal, readOnly}, personal.ID, "", "", "no_writable_team_workspace", nil},
		{"empty list", []canvasWorkspace{}, "", "", "", "no_writable_team_workspace", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(struct {
				Workspaces []canvasWorkspace `json:"workspaces"`
				DefaultID  string            `json:"default_workspace_id,omitempty"`
			}{tc.rows, tc.defaultID})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.RequestURI() != "/api/v1/cli/workspaces" {
					t.Errorf("unexpected selection request %s %s", r.Method, r.URL.Path)
				}
				jsonReply(w, 200, string(body))
			})
			id, options, problem := c.selectCanvasWorkspace(context.Background(), tc.requested)
			if calls.Load() != 1 || id != tc.wantID || (problem == nil) != (tc.wantError == "") ||
				(problem != nil && problem.Code != tc.wantError) {
				t.Fatalf("selection %q, calls %d, error %v", id, calls.Load(), problem)
			}
			if tc.wantOptions != nil && !reflect.DeepEqual(options, tc.wantOptions) {
				t.Fatalf("selectable teams %v, want %v", options, tc.wantOptions)
			}
		})
	}
}

func TestCanvasWorkspaceMalformedListCannotChooseOrCreate(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing list", `{}`},
		{"null list", `{"workspaces":null}`},
		{"bad writable ID", fmt.Sprintf(`{"workspaces":[{"id":%q,"kind":"team","can_create_project":true},{"id":"invalid","kind":"team","can_create_project":true}]}`, canvasTeamOne)},
		{"conflicting workspace permissions", fmt.Sprintf(`{"workspaces":[{"id":%q,"kind":"team","can_create_project":true},{"id":%q,"kind":"team","can_create_project":false}]}`, canvasTeamOne, strings.ToUpper(canvasTeamOne))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.RequestURI() != "/api/v1/cli/workspaces" {
					t.Error("unexpected request after malformed workspace list")
				}
				jsonReply(w, 200, tc.body)
			})
			id, _, problem := c.selectCanvasWorkspace(context.Background(), "")
			if id != "" || problem == nil || problem.Code != "invalid_response" {
				t.Fatalf("selection %q, error %v", id, problem)
			}
		})
	}
}

func TestCanvasWorkspaceRejectsSelectionFromAnotherOrigin(t *testing.T) {
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("forwarded a device bearer to another origin")
		}
		jsonReply(w, 200, fmt.Sprintf(`{"workspaces":[{"id":%q,"name":"Other origin","kind":"team","can_create_project":true}]}`, canvasTeamOne))
	}))
	t.Cleanup(foreign.Close)
	c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/v1/cli/workspaces" || r.Method != "GET" {
			t.Error("unexpected request during workspace selection")
		}
		http.Redirect(w, r, foreign.URL+"/api/v1/cli/workspaces", http.StatusFound)
	})
	id, options, problem := c.selectCanvasWorkspace(context.Background(), "")
	if id != "" || len(options) != 0 || problem == nil {
		t.Fatalf("foreign response supplied selection %q, options %v, error %v", id, options, problem)
	}
}

func TestTeamCanvasCreateBindsWorkspaceTitleKeyAndReceipt(t *testing.T) {
	const title = "成片 · 场景 \"一\""
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.RequestURI() != "/api/v1/cli/projects" ||
					r.Header.Get("Authorization") != "Bearer "+deviceToken || r.Header.Get("Idempotency-Key") != taskID ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
					t.Error("incorrect create request")
				}
				var body map[string]any
				wantBody := map[string]any{"workspace_id": canvasTeamOne, "title": title}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, wantBody) {
					t.Errorf("create payload %v, want %v", body, wantBody)
				}
				jsonReply(w, status, fmt.Sprintf(`{"workspace_id":%q,"project_id":%q,"project_url":"https://elsewhere.invalid/?token=private"}`, strings.ToUpper(canvasTeamOne), strings.ToUpper(projectID)))
			})
			id, problem := c.createTeamCanvas(context.Background(), strings.ToUpper(canvasTeamOne), title, strings.ToUpper(taskID))
			if calls.Load() != 1 || problem != nil || id != projectID {
				t.Fatalf("project %q, calls %d, error %v", id, calls.Load(), problem)
			}
		})
	}
}

func TestTeamCanvasRejectsInvalidCreateReceipts(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"different workspace", fmt.Sprintf(`{"workspace_id":%q,"project_id":%q}`, canvasTeamTwo, projectID)},
		{"missing workspace", fmt.Sprintf(`{"project_id":%q}`, projectID)},
		{"invalid workspace", fmt.Sprintf(`{"workspace_id":"https://x.invalid/opaque","project_id":%q}`, projectID)},
		{"missing project", fmt.Sprintf(`{"workspace_id":%q}`, canvasTeamOne)},
		{"invalid project", fmt.Sprintf(`{"workspace_id":%q,"project_id":"https://x.invalid/opaque"}`, canvasTeamOne)},
		{"nil project", fmt.Sprintf(`{"workspace_id":%q,"project_id":"00000000-0000-0000-0000-000000000000"}`, canvasTeamOne)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.RequestURI() != "/api/v1/cli/projects" {
					t.Error("unexpected fallback request for invalid project receipt")
				}
				jsonReply(w, 201, tc.body)
			})
			id, problem := c.createTeamCanvas(context.Background(), canvasTeamOne, "Project", taskID)
			if id != "" || problem == nil || problem.Code != "invalid_response" || strings.Contains(problem.Message, "x.invalid") {
				t.Fatalf("project %q, error %v", id, problem)
			}
		})
	}
}

func TestTeamCanvasRejectsInvalidRequestIDsBeforeSending(t *testing.T) {
	c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
		t.Error("sent a request with an invalid workspace or idempotency key")
	})
	for _, pair := range [][2]string{{"", taskID}, {"../bad", taskID}, {canvasTeamOne, ""}, {canvasTeamOne, "api-key"}, {canvasTeamOne, "00000000-0000-0000-0000-000000000000"}} {
		if id, problem := c.createTeamCanvas(context.Background(), pair[0], "Project", pair[1]); id != "" || problem == nil {
			t.Fatalf("unexpected project %q or accepted invalid request", id)
		}
	}
}

func TestCanvasWorkspaceAndTeamCanvasRequireDeviceBearer(t *testing.T) {
	for _, key := range []string{"", testKey, deviceTokenPrefix, deviceTokenPrefix + "/invalid"} {
		c := canvasHTTPClient(t, key, func(w http.ResponseWriter, r *http.Request) {
			t.Error("sent a workspace/project request without a valid device bearer")
		})
		_, _, listErr := c.canvasWorkspaces(context.Background())
		_, createErr := c.createTeamCanvas(context.Background(), canvasTeamOne, "Project", taskID)
		for _, problem := range []*failure{listErr, createErr} {
			if problem == nil || problem.Code != "device_login_required" || !strings.Contains(problem.Message, "ifytv auth login") {
				t.Fatalf("expected actionable device sign-in error, got %v", problem)
			}
		}
	}
}

func TestCanvasWorkspaceAndTeamCanvasHandleEndpointErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code, wantCode string
		status               int
	}{
		{"old device allowlist", "device_scope_denied", "canvas_creation_unavailable", 403},
		{"missing collection", "", "canvas_creation_unavailable", 404},
		{"workspace permission", "workspace_forbidden", "workspace_forbidden", 403},
		{"approval lacks workspace scope", "canvas_workspace_scope_required", "canvas_workspace_scope_required", 403},
		{"workspace canvas quota", "workspace_quota_exceeded", "workspace_quota_exceeded", 409},
		{"workspace disappeared", "workspace_not_found", "workspace_not_found", 404},
		{"revoked device", "device_unauthorized", "device_unauthorized", 401},
		{"idempotency conflict", "idempotency_conflict", "idempotency_conflict", 409},
		{"unrecognized server detail", "private_server_detail", "http_error", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.RequestURI() != "/api/v1/cli/workspaces" && r.URL.RequestURI() != "/api/v1/cli/projects" {
					t.Error("unexpected alternate endpoint")
				}
				jsonReply(w, tc.status, fmt.Sprintf(`{"error":{"code":%q,"message":"private_response_text"}}`, tc.code))
			})
			_, _, listErr := c.canvasWorkspaces(context.Background())
			_, createErr := c.createTeamCanvas(context.Background(), canvasTeamOne, "Project", taskID)
			for _, problem := range []*failure{listErr, createErr} {
				if problem == nil || problem.Code != tc.wantCode || problem.HTTPStatus != tc.status ||
					problem.invalidDevice != (tc.code == "device_unauthorized") ||
					strings.Contains(problem.Message, "private") || strings.Contains(problem.Message, deviceToken) {
					t.Fatalf("unexpected endpoint error %v", problem)
				}
				if tc.code == "canvas_workspace_scope_required" && !strings.Contains(problem.Message, "device approval") ||
					tc.code == "workspace_quota_exceeded" && !strings.Contains(problem.Message, "quota") {
					t.Fatalf("missing workspace-specific recovery guidance: %v", problem)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("expected one independent request per endpoint, got %d", calls.Load())
			}
		})
	}
}

func TestCanvasWorkspaceAndTeamCanvasRejectUnexpectedSuccessStatuses(t *testing.T) {
	c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli/workspaces":
			jsonReply(w, 206, `{"workspaces":[]}`)
		case "/api/v1/cli/projects":
			jsonReply(w, 202, fmt.Sprintf(`{"workspace_id":%q,"project_id":%q}`, canvasTeamOne, projectID))
		default:
			t.Error("unexpected alternate endpoint")
		}
	})
	_, _, listErr := c.canvasWorkspaces(context.Background())
	_, createErr := c.createTeamCanvas(context.Background(), canvasTeamOne, "Project", taskID)
	for _, problem := range []*failure{listErr, createErr} {
		if problem == nil || problem.Code != "invalid_response" {
			t.Fatalf("accepted an unexpected success status, error %v", problem)
		}
	}
}

func TestTeamCanvasLostReplyPreservesNetworkUncertainty(t *testing.T) {
	var calls atomic.Int32
	c := canvasHTTPClient(t, deviceToken, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	})
	id, problem := c.createTeamCanvas(context.Background(), canvasTeamOne, "Project", taskID)
	if id != "" || problem == nil || problem.Code != "network_error" || calls.Load() != 1 {
		t.Fatalf("project %q, calls %d, error %v", id, calls.Load(), problem)
	}
}
