package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type productionRun struct {
	ProjectID   string               `json:"project_id"`
	RunID       string               `json:"run_id"`
	EpisodeID   string               `json:"episode_id"`
	PlanVersion uint64               `json:"plan_version"`
	PlanHash    string               `json:"plan_hash"`
	Revision    uint64               `json:"revision"`
	Status      string               `json:"status"`
	NextAction  productionNextAction `json:"next_action"`
	Steps       []productionRunStep  `json:"steps"`
	Billing     json.RawMessage      `json:"billing"`
	Artifact    *productionArtifact  `json:"artifact,omitempty"`
	NextCursor  string               `json:"next_cursor,omitempty"`
}

type productionNextAction struct {
	Type             string   `json:"type"`
	StepIDs          []string `json:"step_ids,omitempty"`
	PollAfterSeconds int      `json:"poll_after_seconds,omitempty"`
}

type productionRunStep struct {
	StepID    string   `json:"step_id"`
	Kind      string   `json:"kind"`
	ShotID    string   `json:"shot_id,omitempty"`
	NodeID    string   `json:"node_id,omitempty"`
	TaskID    string   `json:"task_id,omitempty"`
	Attempt   int      `json:"attempt"`
	Status    string   `json:"status"`
	OutputIDs []string `json:"output_asset_ids"`
	Error     *struct {
		Code string `json:"code"`
	} `json:"error,omitempty"`
}

type productionArtifact struct {
	NodeID     string `json:"node_id,omitempty"`
	AssetID    string `json:"asset_id,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type productionRunList struct {
	Items       []productionRun          `json:"items"`
	LookupState string                   `json:"lookup_state,omitempty"`
	NextCursor  string                   `json:"next_cursor,omitempty"`
	Receipt     *productionLookupReceipt `json:"receipt,omitempty"`
}

type productionLookupReceipt struct {
	RequestID   string `json:"request_id"`
	Operation   string `json:"operation"`
	RunID       string `json:"run_id"`
	RequestHash string `json:"request_sha256"`
}

type productionRunQuote struct {
	QuoteID       string                `json:"quote_id"`
	PlanHash      string                `json:"plan_hash"`
	Currency      string                `json:"currency"`
	ExpiresAt     string                `json:"expires_at"`
	MaxTotalUnits *int64                `json:"max_total_units,omitempty"`
	Lines         []productionQuoteLine `json:"lines"`
}

type productionQuoteLine struct {
	StepID       string `json:"step_id"`
	MaxUnits     *int64 `json:"max_units,omitempty"`
	EstimateKind string `json:"estimate_kind"`
}

type productionQuoteRequest struct {
	PlanHash string   `json:"plan_hash"`
	StepIDs  []string `json:"step_ids,omitempty"`
}

type productionBudget struct {
	Currency      string `json:"currency"`
	MaxTotalUnits int64  `json:"max_total_units"`
}

type productionResumeRequest struct {
	ExpectedRevision uint64            `json:"expected_revision"`
	PlanHash         string            `json:"plan_hash"`
	QuoteID          string            `json:"quote_id,omitempty"`
	Budget           *productionBudget `json:"budget,omitempty"`
	StepIDs          []string          `json:"step_ids,omitempty"`
}

type productionPlan struct {
	EpisodeID   string            `json:"episode_id"`
	PlanVersion uint64            `json:"plan_version"`
	Ratio       string            `json:"ratio"`
	Scenes      []json.RawMessage `json:"scenes"`
	Entities    []json.RawMessage `json:"entities"`
	Shots       []json.RawMessage `json:"shots"`
	Steps       []json.RawMessage `json:"steps"`
}

type productionRunIdentity struct {
	projectID   string
	runID       string
	episodeID   string
	planVersion uint64
	planHash    string
	minRevision uint64
}

func productionRunsPath(project string) string {
	return "/api/v1/cli/projects/" + project + "/production-runs"
}

func invalidProductionRunReply() *failure {
	return fail("invalid_response", "The site did not confirm the requested episode, plan or quote. Check the original request before resuming.")
}

func (r *productionRun) validate(want productionRunIdentity) bool {
	r.ProjectID, r.RunID = strings.ToLower(r.ProjectID), strings.ToLower(r.RunID)
	r.EpisodeID, r.PlanHash = strings.ToLower(r.EpisodeID), strings.ToLower(r.PlanHash)
	if r.ProjectID != want.projectID || !validUUID(r.RunID) || !validUUID(r.EpisodeID) ||
		!validHash(r.PlanHash) || r.PlanVersion == 0 || r.Revision == 0 || r.Revision < want.minRevision ||
		(want.runID != "" && r.RunID != want.runID) ||
		(want.episodeID != "" && r.EpisodeID != want.episodeID) ||
		(want.planVersion != 0 && r.PlanVersion != want.planVersion) ||
		(want.planHash != "" && r.PlanHash != want.planHash) ||
		r.Steps == nil || !jsonObject(r.Billing) || !validProductionNextAction(r.NextAction) ||
		(r.NextCursor != "" && !validProductionCursor(r.NextCursor)) {
		return false
	}
	switch r.Status {
	case "planned", "running", "blocked", "succeeded", "failed", "canceled":
	default:
		return false
	}
	if a := r.Artifact; a != nil {
		a.AssetID, a.SHA256 = strings.ToLower(a.AssetID), strings.ToLower(a.SHA256)
		if (a.NodeID != "" && !nodePattern.MatchString(a.NodeID)) ||
			(a.AssetID != "" && !validUUID(a.AssetID)) ||
			(a.SHA256 != "" && !validHash(a.SHA256)) || a.DurationMS < 0 {
			return false
		}
	}
	if r.Status == "succeeded" && (r.Artifact == nil || r.Artifact.NodeID == "" || r.Artifact.AssetID == "") {
		return false
	}
	seen := make(map[string]bool, len(r.Steps))
	for _, step := range r.Steps {
		if !nodePattern.MatchString(step.StepID) || seen[step.StepID] || !validProductionID(step.Kind) ||
			(step.ShotID != "" && !nodePattern.MatchString(step.ShotID)) ||
			(step.NodeID != "" && !nodePattern.MatchString(step.NodeID)) ||
			(step.TaskID != "" && !validUUID(step.TaskID)) || step.Attempt < 0 ||
			!validProductionStepStatus(step.Status) || step.OutputIDs == nil {
			return false
		}
		for _, asset := range step.OutputIDs {
			if !validUUID(asset) {
				return false
			}
		}
		if step.Error != nil && safeCode(step.Error.Code) == "" {
			return false
		}
		seen[step.StepID] = true
	}
	return true
}

func validProductionNextAction(action productionNextAction) bool {
	switch action.Type {
	case "execute", "poll", "reconcile", "authorize", "fix_input", "review", "done":
	default:
		return false
	}
	if action.PollAfterSeconds < 0 {
		return false
	}
	for _, id := range action.StepIDs {
		if !nodePattern.MatchString(id) {
			return false
		}
	}
	return true
}

func validProductionStepStatus(status string) bool {
	switch status {
	case "waiting_dependencies", "blocked", "reused", "queued", "submitting", "running", "saving", "succeeded", "uncertain", "failed", "canceled":
		return true
	}
	return false
}

func (q *productionRunQuote) validate(hash string) bool {
	q.QuoteID, q.PlanHash = strings.ToLower(q.QuoteID), strings.ToLower(q.PlanHash)
	if !validUUID(q.QuoteID) || q.PlanHash != hash || !validBudgetCurrency(q.Currency) ||
		q.Lines == nil || (q.MaxTotalUnits != nil && *q.MaxTotalUnits < 0) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, q.ExpiresAt); err != nil {
		return false
	}
	seen := make(map[string]bool, len(q.Lines))
	for _, line := range q.Lines {
		if !nodePattern.MatchString(line.StepID) || seen[line.StepID] {
			return false
		}
		switch line.EstimateKind {
		case "exact", "upper_bound":
			if line.MaxUnits == nil || *line.MaxUnits < 0 {
				return false
			}
		case "unknown":
			if line.MaxUnits != nil || q.MaxTotalUnits != nil {
				return false
			}
		default:
			return false
		}
		seen[line.StepID] = true
	}
	return true
}

func jsonObject(data json.RawMessage) bool {
	return len(data) > 0 && data[0] == '{'
}

func validProductionCursor(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func readEpisodePlan(path string) (productionPlan, json.RawMessage, *failure) {
	var plan productionPlan
	f, err := os.Open(path)
	if err != nil {
		return plan, nil, fail("spec_unreadable", "Cannot open the episode plan supplied with --spec.")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return plan, nil, fail("invalid_episode_plan", "Supply an episode plan as a readable regular JSON file.")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	if err != nil || len(raw) > maxJSONBytes || !utf8.Valid(raw) {
		return plan, nil, fail("invalid_episode_plan", "Use a UTF-8 episode plan JSON file of at most 4 MiB.")
	}
	if json.Unmarshal(raw, &plan) != nil || !validUUID(plan.EpisodeID) || plan.PlanVersion == 0 ||
		(plan.Ratio != "9:16" && plan.Ratio != "16:9") || plan.Scenes == nil ||
		plan.Entities == nil || plan.Shots == nil || plan.Steps == nil {
		return plan, nil, fail("invalid_episode_plan", "Use episode_id, plan_version, ratio and arrays for scenes, entities, shots and steps.")
	}
	for _, rows := range [][]json.RawMessage{plan.Scenes, plan.Entities, plan.Shots, plan.Steps} {
		for _, row := range rows {
			if !jsonObject(row) {
				return plan, nil, fail("invalid_episode_plan", "The items in an episode plan must be JSON objects.")
			}
		}
	}
	plan.EpisodeID = strings.ToLower(plan.EpisodeID)
	return plan, raw, nil
}

func (c *client) productionRunCommand(ctx context.Context, o options) (output, *failure) {
	p := o.production
	result := output{RequestID: p.requestID}
	c.addCanvasLocation(&result, o.project, "")
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}
	c = c.productionClient()
	want := productionRunIdentity{
		projectID: o.project, runID: p.runID, episodeID: p.episodeID, planHash: p.planHash, minRevision: o.revision,
	}
	path := productionRunsPath(o.project)
	var problem *failure
	switch o.command {
	case "episode status":
		if p.runID != "" {
			var run *productionRun
			run, problem = c.readProductionRun(ctx, path, want, p.cursor)
			if problem == nil {
				c.addProductionRun(&result, run)
			}
		} else {
			result.EpisodeRuns, problem = c.listProductionRuns(ctx, path, want, p.requestID, p.operation, p.cursor)
		}
	case "episode quote":
		var quote productionRunQuote
		body, _ := json.Marshal(productionQuoteRequest{PlanHash: p.planHash, StepIDs: p.steps})
		problem = c.json(ctx, http.MethodPost, path+"/"+p.runID+"/quote", body, "", true, &quote, http.StatusOK)
		if problem == nil {
			if !quote.validate(want.planHash) {
				problem = invalidProductionRunReply()
			} else {
				result.EpisodeQuote = &quote
			}
		}
	case "episode plan", "episode resume":
		var payload []byte
		postPath := path
		if o.command == "episode plan" {
			plan, raw, specProblem := readEpisodePlan(o.spec)
			if specProblem != nil {
				return result, specProblem
			}
			want.episodeID, want.planVersion = plan.EpisodeID, plan.PlanVersion
			payload, _ = json.Marshal(struct {
				ExpectedRevision uint64          `json:"expected_revision"`
				Plan             json.RawMessage `json:"plan"`
			}{o.revision, raw})
		} else {
			postPath = path + "/" + p.runID + "/resume"
			request := productionResumeRequest{
				ExpectedRevision: o.revision, PlanHash: p.planHash, QuoteID: p.quoteID, StepIDs: p.steps,
			}
			if p.budget != nil {
				request.Budget = &productionBudget{Currency: p.currency, MaxTotalUnits: *p.budget}
			}
			payload, _ = json.Marshal(request)
		}
		var run *productionRun
		run, problem = c.mutateProductionRun(ctx, o, postPath, payload, want)
		if run != nil {
			c.addProductionRun(&result, run)
		}
	}
	if problem != nil {
		c.addProjectConsent(&result, problem)
		if problem.HTTPStatus == http.StatusNotFound && problem.Code == "http_error" {
			problem.Code = "episode_orchestration_unavailable"
			problem.Message = "This site has not exposed cloud episode orchestration to this device. Keep the project and request IDs."
		}
	}
	return result, problem
}

func (c *client) addProductionRun(result *output, run *productionRun) {
	node := ""
	if run.Artifact != nil && run.Status == "succeeded" {
		node, result.AssetID = run.Artifact.NodeID, run.Artifact.AssetID
	}
	c.addCanvasLocation(result, run.ProjectID, node)
	result.EpisodeRun, result.Status = run, run.Status
}

func (c *client) readProductionRun(ctx context.Context, path string, want productionRunIdentity, cursor string) (*productionRun, *failure) {
	var run productionRun
	path += "/" + want.runID
	if cursor != "" {
		path += "?" + url.Values{"cursor": {cursor}}.Encode()
	}
	if problem := c.json(ctx, http.MethodGet, path, nil, "", true, &run, http.StatusOK); problem != nil {
		return nil, problem
	}
	if !run.validate(want) {
		return nil, invalidProductionRunReply()
	}
	return &run, nil
}

func (c *client) listProductionRuns(ctx context.Context, path string, want productionRunIdentity, id, operation, cursor string) (*productionRunList, *failure) {
	params := make(url.Values)
	if want.episodeID != "" && id == "" {
		params.Set("episode_id", want.episodeID)
	}
	if id != "" {
		params.Set("request_id", id)
		params.Set("operation", operation)
	}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	if len(params) != 0 {
		path += "?" + params.Encode()
	}
	var result productionRunList
	if problem := c.json(ctx, http.MethodGet, path, nil, "", true, &result, http.StatusOK); problem != nil {
		return nil, problem
	}
	if result.Items == nil || (result.NextCursor != "" && !validProductionCursor(result.NextCursor)) {
		return nil, invalidProductionRunReply()
	}
	if id != "" {
		switch result.LookupState {
		case "accepted":
			receipt := result.Receipt
			if len(result.Items) != 1 || result.NextCursor != "" || receipt == nil ||
				!strings.EqualFold(receipt.RequestID, id) || receipt.Operation != operation ||
				!strings.EqualFold(receipt.RunID, result.Items[0].RunID) || !validHash(receipt.RequestHash) {
				return nil, invalidProductionRunReply()
			}
			receipt.RequestID, receipt.RunID = id, strings.ToLower(receipt.RunID)
			receipt.RequestHash = strings.ToLower(receipt.RequestHash)
		case "not_accepted", "uncertain":
			if len(result.Items) != 0 || result.NextCursor != "" || result.Receipt != nil {
				return nil, invalidProductionRunReply()
			}
		default:
			return nil, invalidProductionRunReply()
		}
	}
	for i := range result.Items {
		if !result.Items[i].validate(want) {
			return nil, invalidProductionRunReply()
		}
	}
	return &result, nil
}
