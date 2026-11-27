package ifytvcli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type canvasWorkspace struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	CanCreateProject bool   `json:"can_create_project"`
}

func (c *client) canvasWorkspaces(ctx context.Context) ([]canvasWorkspace, string, *failure) {
	if problem := c.requireCanvasDevice(); problem != nil {
		return nil, "", problem
	}
	var reply struct {
		Workspaces []canvasWorkspace `json:"workspaces"`
		DefaultID  string            `json:"default_workspace_id"`
	}
	if problem := c.json(ctx, http.MethodGet, "/api/v1/cli/workspaces", nil, "", true, &reply, http.StatusOK); problem != nil {
		return nil, "", canvasCollectionFailure(problem, teamWorkspaceKind)
	}
	if reply.Workspaces == nil {
		return nil, "", fail("invalid_response", "The site did not return a workspace list. Retry the same command.")
	}
	eligible := make([]canvasWorkspace, 0, len(reply.Workspaces))
	seen := make(map[string]bool, len(reply.Workspaces))
	var defaultID string
	for _, workspace := range reply.Workspaces {
		workspace.ID = strings.ToLower(workspace.ID)
		if !validUUID(workspace.ID) || seen[workspace.ID] {
			return nil, "", fail("invalid_response", "The site returned invalid or repeated workspace IDs. Retry the same command.")
		}
		seen[workspace.ID] = true
		if workspace.Kind == teamWorkspaceKind && workspace.CanCreateProject {
			eligible = append(eligible, workspace)
			if strings.EqualFold(reply.DefaultID, workspace.ID) {
				defaultID = workspace.ID
			}
		}
	}
	return eligible, defaultID, nil
}

func (c *client) selectCanvasWorkspace(ctx context.Context, requested string) (string, []canvasWorkspace, *failure) {
	if requested == "" {
		return "", nil, nil
	}
	eligible, _, problem := c.canvasWorkspaces(ctx)
	if problem != nil {
		return "", nil, problem
	}
	if len(eligible) == 0 {
		return "", eligible, fail("no_writable_team_workspace", "This account needs permission to create canvases in a team workspace.")
	}
	for _, workspace := range eligible {
		if strings.EqualFold(requested, workspace.ID) {
			return workspace.ID, nil, nil
		}
	}
	return "", eligible, fail("workspace_not_writable", "Check creation permission for the requested team. To use a different team, choose a new run name and pass --workspace UUID.")
}

type canvasProjectReceipt struct {
	ProjectID     string
	WorkspaceID   string
	WorkspaceKind string
}

// With no team UUID, the site resolves the personal workspace from the device bearer.
func (c *client) createWorkspaceCanvas(ctx context.Context, workspaceID, title, key string) (canvasProjectReceipt, *failure) {
	if problem := c.requireCanvasDevice(); problem != nil {
		return canvasProjectReceipt{}, problem
	}
	if !validUUID(key) {
		return canvasProjectReceipt{}, fail("invalid_idempotency_key", "The canvas creation request needs a valid idempotency UUID.")
	}
	var reply struct {
		ProjectID     string  `json:"project_id"`
		WorkspaceID   string  `json:"workspace_id"`
		WorkspaceKind *string `json:"workspace_kind"`
	}
	path, kind := "/api/v1/cli/projects/personal", personalWorkspaceKind
	if workspaceID != "" {
		if !validUUID(workspaceID) {
			return canvasProjectReceipt{}, fail("invalid_workspace", "Supply a team workspace UUID.")
		}
		workspaceID = strings.ToLower(workspaceID)
		path, kind = "/api/v1/cli/projects", teamWorkspaceKind
		// Older team endpoints omit the kind. An explicit JSON null still fails validation.
		legacyKind := teamWorkspaceKind
		reply.WorkspaceKind = &legacyKind
	}
	payload, _ := json.Marshal(struct {
		WorkspaceID string `json:"workspace_id,omitempty"`
		Title       string `json:"title"`
	}{workspaceID, title})
	if problem := c.json(ctx, http.MethodPost, path, payload, strings.ToLower(key), true, &reply,
		http.StatusCreated, http.StatusOK); problem != nil {
		return canvasProjectReceipt{}, canvasCollectionFailure(problem, kind)
	}
	if !validUUID(reply.ProjectID) || !validUUID(reply.WorkspaceID) || reply.WorkspaceKind == nil || *reply.WorkspaceKind != kind ||
		(workspaceID != "" && !strings.EqualFold(reply.WorkspaceID, workspaceID)) {
		return canvasProjectReceipt{}, fail("invalid_response", "The site did not confirm a project in the requested "+kind+" workspace. Retry the same run.")
	}
	return canvasProjectReceipt{ProjectID: strings.ToLower(reply.ProjectID), WorkspaceID: strings.ToLower(reply.WorkspaceID), WorkspaceKind: kind}, nil
}

func (c *client) requireCanvasDevice() *failure {
	if !validDeviceToken(c.key) {
		return fail("device_login_required", "Canvas creation and workspace lookup require a device sign-in. Unset IFYTV_API_KEY and run ifytv auth login.")
	}
	return nil
}

func canvasCollectionFailure(problem *failure, kind string) *failure {
	if problem.HTTPStatus < 300 {
		return problem
	}
	if (problem.HTTPStatus == http.StatusForbidden && problem.Code == "device_scope_denied") ||
		(problem.HTTPStatus == http.StatusNotFound && problem.Code == "http_error") {
		problem.Code = "canvas_creation_unavailable"
		problem.Message = "Ask the site administrator to enable CLI " + kind + " canvas creation, then retry the same command."
		return problem
	}
	// Only publish known protocol codes. An arbitrary error code can contain
	// private server data even when it passes the generic client's syntax check.
	switch problem.Code {
	case "device_unauthorized", "unauthorized":
		problem.Message = "The device sign-in is invalid or expired. Sign in with ifytv auth login."
	case "canvas_workspace_scope_required":
		problem.Message = "The device approval lacks permission to create " + kind + " canvases. Reauthorize the device with that permission, then retry the same run."
	case "workspace_forbidden", "workspace_not_writable", "canvas_forbidden", "forbidden":
		problem.Message = "This account cannot create a canvas in the selected " + kind + " workspace. Check its project creation permissions."
	case "personal_workspace_unavailable":
		problem.Message = "The signed-in account's personal workspace is unavailable. Ask the site administrator to restore access, then retry the same run."
	case "workspace_quota_exceeded":
		problem.Message = "The " + kind + " workspace has reached its canvas quota. Retry the same run when the workspace has room for another canvas."
	case "workspace_not_found", "canvas_not_found", "idempotency_conflict", "rate_limited",
		"invalid_workspace", "invalid_title", "invalid_idempotency_key", "canvas_timeout", "canvas_internal_error":
		// The shared HTTP client supplies the message and the retry interval.
	default:
		problem.Code = "http_error"
	}
	return problem
}
