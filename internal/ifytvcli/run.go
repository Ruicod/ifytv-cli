package ifytvcli

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"time"
)

type canvasRunRequest struct {
	Spec             json.RawMessage `json:"spec"`
	Names            []string        `json:"reference_names"`
	ExpectedRevision uint64          `json:"expected_revision,omitempty"`
	TargetNodeID     string          `json:"target_node_id,omitempty"`
}

func runConflict() *failure {
	return fail("run_conflict", "The spec or reference contents changed. Restore the original inputs or choose a new run name.")
}

func (c *client) create(ctx context.Context, o options) (output, *failure) {
	result := output{Run: o.run, ProjectID: o.project}
	if o.revision != 0 {
		c.addCanvasLocation(&result, o.project, o.node)
	}
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
	if r != nil && r.Version == canvasContinuationVersion {
		c.addCanvasLocation(&result, r.ProjectID, r.TargetNodeID)
	}
	if problem := file.bindCanvasDestination(r, o); problem != nil {
		result.WorkspaceID, result.WorkspaceKind = r.WorkspaceID, r.WorkspaceKind
		return result, problem
	}
	continuation := o.revision != 0 || (r != nil && r.Version == canvasContinuationVersion)
	if continuation {
		if problem := c.requireProductionDevice(o); problem != nil {
			return result, problem
		}
		c = c.productionClient()
	}
	if r == nil {
		// Preserve the destination even if the model catalog or references are unavailable.
		// A bare retry resumes the original canvas or direct task.
		r, problem = file.reserveVideo(o)
		if problem != nil {
			return result, problem
		}
	}
	result.WorkspaceID, result.WorkspaceKind = r.WorkspaceID, r.WorkspaceKind
	c.addCanvasLocation(&result, r.ProjectID, r.TargetNodeID)
	refs := spec.references()
	hasSpec := r.SpecHash != ""
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
		if continuation {
			if refs[i].path == "" {
				return result, fail("invalid_reference", refs[i].label+" must be a local file for canvas continuation.")
			}
			if committed {
				if _, ok := c.productionUploadPath(r.ProjectID, r.Refs[i].URL); !ok {
					return result, corruptState()
				}
			}
		}
		if hasSpec && refs[i].hash != "" && refs[i].hash != r.Refs[i].Hash {
			return result, runConflict()
		}
	}
	if r.Task != nil {
		result.TaskID, result.Task = r.Task.ID, r.Task.Task
		c.addCanvasLocation(&result, r.ProjectID, r.Task.NodeID)
		return withTaskStatus(result)
	}
	if r.ProjectKey != "" && r.ProjectID == "" {
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
			if r.Key == "" {
				key, err := newKey()
				if err != nil {
					return result, fail("state_unavailable", "Cannot generate a secure idempotency key.")
				}
				r.Key = key
			}
			r.Stage, r.SpecHash = "uploading", specHash
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
			if problem := c.ensureCanvas(ctx, file, r, &result); problem != nil {
				return result, problem
			}
			c.report("Canvas: " + result.ProjectURL)
		}
		if continuation {
			if problem := c.stageContinuationReferences(ctx, refs, r, file, &result); problem != nil {
				return result, problem
			}
		} else if problem := c.uploadMissing(ctx, refs, r, file); problem != nil {
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
			payload, err = json.Marshal(canvasRunRequest{
				Spec: payload, Names: names, ExpectedRevision: r.ExpectedRevision, TargetNodeID: r.TargetNodeID,
			})
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
	switch {
	case continuation:
		path = "/api/v1/cli/projects/" + r.ProjectID + "/continuations"
	case r.ProjectID != "":
		path = "/api/v1/cli/projects/" + r.ProjectID + "/runs"
	}
	if problem := c.json(ctx, http.MethodPost, path, r.Payload, r.Key, true, &reply); problem != nil {
		if r.ProjectID != "" {
			c.addProjectConsent(&result, problem)
		}
		if continuation && problem.HTTPStatus == http.StatusConflict && problem.Code == "canvas_revision_changed" {
			problem.Message = "The canvas no longer matches the saved revision. Inspect the project again before preparing another continuation."
		}
		return result, problem
	}
	if problem := r.validateTask(&reply); problem != nil {
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

func (r *record) validateTask(reply *taskReply) *failure {
	if problem := reply.validate(""); problem != nil {
		return problem
	}
	if problem := reply.validateProject(r.ProjectID); problem != nil {
		return problem
	}
	if r.TargetNodeID != "" && reply.NodeID != r.TargetNodeID {
		return fail("invalid_response", "The site returned a different video card from the saved continuation. Retry the same run.")
	}
	return nil
}

func (f *stateFile) reserveVideo(o options) (*record, *failure) {
	if o.revision != 0 {
		key, err := newKey()
		if err != nil {
			return nil, fail("state_unavailable", "Cannot generate a secure idempotency key.")
		}
		r := &record{Version: canvasContinuationVersion, Stage: "video", Key: key,
			ProjectID: o.project, ExpectedRevision: o.revision, TargetNodeID: o.node}
		return r, f.save(r)
	}
	if o.project == "" && !o.noCanvas {
		return f.reserveCanvas("video", o.workspace)
	}
	r := &record{Version: 1, Stage: "video"}
	if o.project != "" {
		r.Version, r.ProjectID = 2, o.project
	}
	return r, f.save(r)
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

func (c *client) stageContinuationReferences(ctx context.Context, refs []reference, r *record, file *stateFile, result *output) *failure {
	for i, ref := range refs {
		if saved := r.Refs[i].URL; saved != "" {
			path, ok := c.productionUploadPath(r.ProjectID, saved)
			if !ok {
				return corruptState()
			}
			available, problem := c.checkContinuationUpload(ctx, path, ref)
			if problem != nil {
				c.addProjectConsent(result, problem)
				return problem
			}
			if available {
				continue
			}
			if ref.hash == "" {
				return fail("missing_reference", ref.label+" is no longer available. Restore the original local file and retry the same run.")
			}
		}
		staged, problem := c.stageProductionMedia(ctx, result, ref)
		if problem != nil {
			return problem
		}
		r.Refs[i].URL = staged
		if problem := file.save(r); problem != nil {
			return problem
		}
		c.report("Uploaded " + ref.label + ".")
	}
	return nil
}

func (c client) checkContinuationUpload(ctx context.Context, path string, ref reference) (bool, *failure) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, problem := c.request(probeCtx, http.MethodGet, path, nil, true)
	if problem != nil {
		return false, problem
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")
	// The saved staging route is the only resource this probe can read.
	httpClient := *c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirect }
	c.http = &httpClient
	resp, problem := c.send(req)
	if problem != nil {
		if problem.HTTPStatus == http.StatusNotFound || problem.HTTPStatus == http.StatusGone {
			return false, nil
		}
		return false, problem
	}
	defer resp.Body.Close()
	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) &&
		(contentType == ref.mime || contentType == "application/octet-stream") {
		var first [1]byte
		if _, err := io.ReadFull(resp.Body, first[:]); err == nil {
			return true, nil
		}
	}
	if ctx.Err() != nil {
		return false, contextFailure(ctx)
	}
	return false, fail("upload_check_unavailable", "Cannot verify the saved private upload for "+ref.label+". Retry the same run.")
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
