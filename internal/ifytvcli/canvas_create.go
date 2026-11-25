package ifytvcli

import "context"

// Site and video identity already scope the run file. Its destination becomes
// immutable as soon as a project request or a video run is checkpointed.
func checkCanvasDestination(r *record, o options) *failure {
	if r == nil {
		return nil
	}
	if (o.project != "" && o.project != r.ProjectID) ||
		(o.workspace != "" && (r.ProjectKey == "" || (r.WorkspaceID != "" && o.workspace != r.WorkspaceID))) ||
		(o.autoProject && r.ProjectID == "" && r.ProjectKey == "") {
		return fail("run_project_conflict", "This name belongs to a different destination. Resume the original run or choose a new name for a separate video.")
	}
	return nil
}

func (f *stateFile) reserveCanvas(stage, requested string) (*record, *failure) {
	key, err := newKey()
	if err != nil {
		return nil, fail("state_unavailable", "Cannot generate a secure project idempotency key.")
	}
	r := &record{Version: 3, Stage: stage, RequestedWorkspace: requested, ProjectKey: key}
	return r, f.save(r)
}

func (c *client) ensureCanvas(ctx context.Context, file *stateFile, r *record, requested string, result *output) *failure {
	if r.ProjectKey != "" && r.WorkspaceID == "" {
		if requested != "" && requested != r.RequestedWorkspace {
			r.RequestedWorkspace = requested
			if problem := file.save(r); problem != nil {
				return problem
			}
		}
		id, choices, problem := c.selectCanvasWorkspace(ctx, r.RequestedWorkspace)
		if problem != nil {
			if choices != nil {
				result.Workspaces = &choices
			}
			return problem
		}
		r.WorkspaceID, r.RequestedWorkspace = id, ""
		// Freeze the selected team before the first POST could reach the site.
		if problem := file.save(r); problem != nil {
			return problem
		}
	}
	result.WorkspaceID = r.WorkspaceID
	if r.ProjectID == "" {
		id, problem := c.createTeamCanvas(ctx, r.WorkspaceID, result.Run, r.ProjectKey)
		if problem != nil {
			return problem
		}
		r.ProjectID = id
		if problem := file.save(r); problem != nil {
			c.addCanvasLocation(result, id, "")
			return problem
		}
	}
	c.addCanvasLocation(result, r.ProjectID, "")
	return nil
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
	if problem := checkCanvasDestination(r, o); problem != nil {
		return result, problem
	}
	if r == nil {
		r, problem = file.reserveCanvas("canvas", o.workspace)
		if problem != nil {
			return result, problem
		}
	}
	problem = c.ensureCanvas(ctx, file, r, o.workspace, &result)
	return result, problem
}
