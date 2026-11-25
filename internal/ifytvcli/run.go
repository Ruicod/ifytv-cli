package ifytvcli

import (
	"context"
	"encoding/json"
	"path/filepath"
)

type canvasRunRequest struct {
	Spec  json.RawMessage `json:"spec"`
	Names []string        `json:"reference_names"`
}

func runConflict() *failure {
	return fail("run_conflict", "The spec or reference contents changed. Restore the original inputs or choose a new run name.")
}

func (c *client) create(ctx context.Context, o options) (output, *failure) {
	result := output{Run: o.run, ProjectID: o.project}
	spec, specHash, problem := readSpec(o.spec)
	if problem != nil {
		return result, problem
	}
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
	if r != nil {
		o.project = r.ProjectID
		result.WorkspaceID = r.WorkspaceID
	}
	c.addCanvasLocation(&result, o.project, "")
	refs := spec.references()
	hasSpec := r != nil && r.SpecHash != ""
	if hasSpec && (r.SpecHash != specHash || len(r.Refs) != len(refs)) {
		return result, runConflict()
	}
	// A saved receipt permits an absent local file unless it needs uploading
	// again. Any local file still present must match the original digest.
	for i := range refs {
		committed := hasSpec && r.Refs[i].URL != ""
		if problem := refs[i].inspect(filepath.Dir(o.spec), committed); problem != nil {
			return result, problem
		}
		if hasSpec && refs[i].hash != "" && refs[i].hash != r.Refs[i].Hash {
			return result, runConflict()
		}
	}
	if r != nil && r.Task != nil {
		result.TaskID, result.Task = r.Task.ID, r.Task.Task
		c.addCanvasLocation(&result, r.ProjectID, r.Task.NodeID)
		return withTaskStatus(result)
	}
	// Remember an on-canvas request before discovery. A catalog outage or a
	// choice between teams must never turn a same-name retry into a direct task.
	if r == nil && o.autoProject {
		r, problem = file.reserveCanvas("video", o.workspace)
		if problem != nil {
			return result, problem
		}
	}
	if r != nil && r.ProjectKey != "" && r.ProjectID == "" {
		if problem := c.requireCanvasDevice(); problem != nil {
			return result, problem
		}
	}
	if !hasSpec || r.Stage == "uploading" {
		models, problem := c.models(ctx)
		if problem != nil {
			return result, problem
		}
		if problem := spec.validate(models); problem != nil {
			return result, problem
		}
		if !hasSpec {
			key, err := newKey()
			if err != nil {
				return result, fail("state_unavailable", "Cannot generate a secure idempotency key.")
			}
			if r == nil {
				if o.project != "" {
					r = &record{Version: 2, ProjectID: o.project}
				} else {
					r = &record{Version: 1}
				}
			}
			r.Stage, r.Key, r.SpecHash = "uploading", key, specHash
			r.Refs = make([]savedRef, len(refs))
			for i, ref := range refs {
				r.Refs[i].Hash = ref.hash
				if ref.path == "" {
					r.Refs[i].URL = *ref.value
				}
			}
			if problem := file.save(r); problem != nil {
				return result, problem
			}
		}
		if r.ProjectKey != "" {
			if problem := c.ensureCanvas(ctx, file, r, o.workspace, &result); problem != nil {
				return result, problem
			}
			c.report("Canvas: " + result.ProjectURL)
		}
		if problem := c.uploadMissing(ctx, refs, r, file); problem != nil {
			return result, problem
		}
		for i, ref := range refs {
			*ref.value = r.Refs[i].URL
		}
		payload, err := json.Marshal(spec)
		if err == nil && r.ProjectID != "" {
			names := make([]string, len(refs))
			for i, ref := range refs {
				names[i] = ref.label
				if ref.path != "" {
					names[i] = filepath.Base(ref.path)
				}
			}
			payload, err = json.Marshal(canvasRunRequest{Spec: payload, Names: names})
		}
		if err != nil {
			return result, fail("invalid_spec", "Could not encode the resolved spec.")
		}
		r.Payload, r.Stage = payload, "prepared"
		if problem := file.save(r); problem != nil {
			return result, problem
		}
	}
	// From this point on even a lost or malformed response is ambiguous.
	// Every retry sends only the previously committed key and exact bytes.
	var reply taskReply
	path := "/api/v1/tasks"
	if r.ProjectID != "" {
		path = "/api/v1/cli/projects/" + r.ProjectID + "/runs"
	}
	if problem := c.json(ctx, "POST", path, r.Payload, r.Key, true, &reply); problem != nil {
		if r.ProjectID != "" {
			c.addProjectConsent(&result, problem)
		}
		return result, problem
	}
	if problem := reply.validate(""); problem != nil {
		return result, problem
	}
	if problem := reply.validateProject(r.ProjectID); problem != nil {
		return result, problem
	}
	r.Task, r.Stage = &reply, "submitted"
	result.TaskID, result.Task = reply.ID, reply.Task
	c.addCanvasLocation(&result, r.ProjectID, reply.NodeID)
	if problem := file.save(r); problem != nil {
		return result, problem
	}
	return withTaskStatus(result)
}

func withTaskStatus(result output) (output, *failure) {
	if result.Status == "failed" {
		reason := "Video generation failed."
		if result.ErrorCode != "" {
			reason = "Video generation failed (" + result.ErrorCode + ")."
		}
		return result, fail("task_failed", reason)
	}
	return result, nil
}

func (c *client) uploadMissing(ctx context.Context, refs []reference, r *record, file *stateFile) *failure {
	const parallelism = 4 // The gateway's default concurrent upload limit.
	type completed struct {
		index   int
		url     string
		problem *failure
	}
	pending := make([]int, 0, len(refs))
	for i, ref := range refs {
		if saved := r.Refs[i].URL; saved != "" {
			if ref.path == "" {
				continue // The original spec supplied this remote URL.
			}
			available, problem := c.checkUpload(ctx, saved, ref.label)
			if problem != nil {
				return problem
			}
			if available {
				continue
			}
			if ref.hash == "" {
				return fail("missing_reference", ref.label+" is no longer available. Restore the original local file and retry the same run.")
			}
		}
		// Keep the previous receipt on disk until a replacement is checkpointed.
		pending = append(pending, i)
	}
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return contextFailure(ctx)
		}
		n := min(parallelism, len(pending))
		done := make(chan completed, n)
		for _, i := range pending[:n] {
			go func(i int) {
				url, problem := c.uploadWithRetry(ctx, refs[i])
				done <- completed{i, url, problem}
			}(i)
		}
		var first *failure
		firstIndex := len(refs)
		for range n {
			res := <-done
			if res.problem == nil {
				r.Refs[res.index].URL = res.url
				res.problem = file.save(r)
				if res.problem == nil {
					c.report("Uploaded " + refs[res.index].label + ".")
				}
			}
			if res.problem != nil && res.index < firstIndex {
				firstIndex, first = res.index, res.problem
			}
		}
		if first != nil {
			return first
		}
		pending = pending[n:]
	}
	return nil
}
