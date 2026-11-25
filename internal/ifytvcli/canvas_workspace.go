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
		return nil, "", canvasCollectionFailure(problem)
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
		if workspace.Kind == "team" && workspace.CanCreateProject {
			eligible = append(eligible, workspace)
			if strings.EqualFold(reply.DefaultID, workspace.ID) {
				defaultID = workspace.ID
			}
		}
	}
	return eligible, defaultID, nil
}

func (c *client) selectCanvasWorkspace(ctx context.Context, requested string) (string, []canvasWorkspace, *failure) {
	eligible, defaultID, problem := c.canvasWorkspaces(ctx)
	if problem != nil {
		return "", nil, problem
	}
	if len(eligible) == 0 {
		return "", eligible, fail("no_writable_team_workspace", "This account needs permission to create canvases in a team workspace.")
	}
	if requested != "" {
		for _, workspace := range eligible {
			if strings.EqualFold(requested, workspace.ID) {
				return workspace.ID, nil, nil
			}
		}
		return "", eligible, fail("workspace_not_writable", "Choose a writable team workspace with --workspace UUID.")
	}
	if defaultID != "" {
		return defaultID, nil, nil
	}
	if len(eligible) == 1 {
		return eligible[0].ID, nil, nil
	}
	return "", eligible, fail("workspace_selection_required", "Choose a team workspace and retry the same command with --workspace UUID.")
}

func (c *client) createTeamCanvas(ctx context.Context, workspaceID, title, key string) (string, *failure) {
	if problem := c.requireCanvasDevice(); problem != nil {
		return "", problem
	}
	if !validUUID(workspaceID) {
		return "", fail("invalid_workspace", "Supply a team workspace UUID.")
	}
	if !validUUID(key) {
		return "", fail("invalid_idempotency_key", "The canvas creation request needs a valid idempotency UUID.")
	}
	workspaceID = strings.ToLower(workspaceID)
	payload, _ := json.Marshal(struct {
		WorkspaceID string `json:"workspace_id"`
		Title       string `json:"title"`
	}{workspaceID, title})
	var reply struct {
		ProjectID   string `json:"project_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if problem := c.json(ctx, http.MethodPost, "/api/v1/cli/projects", payload, strings.ToLower(key), true, &reply,
		http.StatusCreated, http.StatusOK); problem != nil {
		return "", canvasCollectionFailure(problem)
	}
	if !strings.EqualFold(reply.WorkspaceID, workspaceID) || !validUUID(reply.ProjectID) {
		return "", fail("invalid_response", "The site did not confirm a project in the requested workspace. Retry the same run.")
	}
	return strings.ToLower(reply.ProjectID), nil
}

func (c *client) requireCanvasDevice() *failure {
	if !validDeviceToken(c.key) {
		return fail("device_login_required", "Unset IFYTV_API_KEY and sign in with ifytv auth login to use team canvas creation.")
	}
	return nil
}

func canvasCollectionFailure(problem *failure) *failure {
	if problem.HTTPStatus < 300 {
		return problem
	}
	if (problem.HTTPStatus == http.StatusForbidden && problem.Code == "device_scope_denied") ||
		(problem.HTTPStatus == http.StatusNotFound && problem.Code == "http_error") {
		problem.Code = "canvas_creation_unavailable"
		problem.Message = "Ask the site administrator to enable CLI team canvas creation, then retry the same command."
		return problem
	}
	// Only publish known protocol codes. An arbitrary error code can contain
	// private server data even when it passes the generic client's syntax check.
	switch problem.Code {
	case "device_unauthorized", "unauthorized":
		problem.Message = "The device sign-in is invalid or expired. Sign in with ifytv auth login."
	case "canvas_workspace_scope_required":
		problem.Message = "The device approval lacks permission to create team canvases. Reauthorize the device with that permission, then retry the same run."
	case "workspace_forbidden", "workspace_not_writable", "canvas_forbidden", "forbidden":
		problem.Message = "This account cannot create a canvas in that team workspace. Check the team's permissions."
	case "workspace_quota_exceeded":
		problem.Message = "The team workspace has reached its canvas quota. Retry the same run when the workspace has room for another canvas."
	case "workspace_not_found", "canvas_not_found", "idempotency_conflict", "rate_limited",
		"invalid_workspace", "invalid_title", "invalid_idempotency_key", "canvas_timeout", "canvas_internal_error":
		// The shared HTTP client supplies the message and the retry interval.
	default:
		problem.Code = "http_error"
	}
	return problem
}
