package ifytvcli

import "context"

const personalWorkspaceKind = "personal"
const teamWorkspaceKind = "team"

// Site and video identity already scope the run file. Its destination becomes
// immutable as soon as a project request or a video run is checkpointed.
func (f *stateFile) bindCanvasDestination(r *record, o options) *failure {
	if r == nil {
		return nil
	}
	if (o.project != "" && o.project != r.ProjectID) ||
		(o.noCanvas && r.Version != 1) || (o.autoProject && r.Version == 1) {
		return destinationConflict()
	}
	if r.Version == 3 && r.ProjectID == "" {
		// The team-only preview saved the chosen workspace before its first
		// POST, but didn't record whether it was chosen by the user or by default.
		if r.WorkspaceID != "" && o.workspace == "" {
			return fail("legacy_team_confirmation_required", "An earlier CLI may have created this run in team "+r.WorkspaceID+". To recover it there, confirm the team and retry with --workspace "+r.WorkspaceID+".")
		}
		target := r.WorkspaceID
		if target == "" {
			target = r.RequestedWorkspace
		}
		if target == "" {
			r.setNewCanvasWorkspace(o.workspace)
		} else {
			if o.workspace != "" && o.workspace != target {
				return destinationConflict()
			}
			r.WorkspaceKind = teamWorkspaceKind
		}
		r.Version = 4
		return f.save(r)
	}
	if o.workspace == personalWorkspaceKind {
		if r.WorkspaceKind != personalWorkspaceKind {
			return destinationConflict()
		}
	} else if o.workspace != "" && (r.WorkspaceKind == personalWorkspaceKind || (o.workspace != r.WorkspaceID && o.workspace != r.RequestedWorkspace)) {
		return destinationConflict()
	}
	return nil
}

func destinationConflict() *failure {
	return fail("run_project_conflict", "This name belongs to a different destination. Resume the original run or choose a new name for a separate video.")
}

func (r *record) setNewCanvasWorkspace(requested string) {
	r.WorkspaceKind = personalWorkspaceKind
	if requested != "" && requested != personalWorkspaceKind {
		r.WorkspaceKind, r.RequestedWorkspace = teamWorkspaceKind, requested
	}
}

func (f *stateFile) reserveCanvas(stage, requested string) (*record, *failure) {
	key, err := newKey()
	if err != nil {
		return nil, fail("state_unavailable", "Cannot generate a secure project idempotency key.")
	}
	r := &record{Version: 4, Stage: stage, ProjectKey: key}
	r.setNewCanvasWorkspace(requested)
	return r, f.save(r)
}

func (c *client) ensureCanvas(ctx context.Context, file *stateFile, r *record, result *output) *failure {
	result.WorkspaceID, result.WorkspaceKind = r.WorkspaceID, r.WorkspaceKind
	if r.ProjectID != "" {
		c.addCanvasLocation(result, r.ProjectID, "")
		return nil
	}
	if r.WorkspaceKind == teamWorkspaceKind && r.WorkspaceID == "" {
		id, choices, problem := c.selectCanvasWorkspace(ctx, r.RequestedWorkspace)
		if problem != nil {
			if choices != nil {
				result.Workspaces = &choices
			}
			return problem
		}
		r.WorkspaceID, r.RequestedWorkspace = id, ""
		result.WorkspaceID = id
		// Freeze the selected team before the first POST could reach the site.
		if problem := file.save(r); problem != nil {
			return problem
		}
	}
	receipt, problem := c.createWorkspaceCanvas(ctx, r.WorkspaceID, result.Run, r.ProjectKey)
	if problem != nil {
		return problem
	}
	r.WorkspaceID, r.ProjectID = receipt.WorkspaceID, receipt.ProjectID
	result.WorkspaceID = r.WorkspaceID
	c.addCanvasLocation(result, r.ProjectID, "")
	return file.save(r)
}

func (c *client) createCanvasForRun(ctx context.Context, o options) (output, *failure) {
	result := output{Run: o.run}
	file, problem := c.lockRun(ctx, o.run)
	if problem != nil {
		return result, problem
	}
	defer file.unlock()
	r, problem := file.load()
	if problem != nil {
		return result, problem
	}
	if r != nil {
		result.WorkspaceID, result.WorkspaceKind = r.WorkspaceID, r.WorkspaceKind
	}
	if problem := file.bindCanvasDestination(r, o); problem != nil {
		return result, problem
	}
	if r == nil {
		r, problem = file.reserveCanvas("canvas", o.workspace)
		if problem != nil {
			return result, problem
		}
	}
	problem = c.ensureCanvas(ctx, file, r, &result)
	return result, problem
}
