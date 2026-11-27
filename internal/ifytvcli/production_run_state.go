package ifytvcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

type productionRequestRecord struct {
	Version     int    `json:"version"`
	RequestHash string `json:"request_sha256"`
	RunID       string `json:"run_id,omitempty"`
	EpisodeID   string `json:"episode_id,omitempty"`
	PlanVersion uint64 `json:"plan_version,omitempty"`
	PlanHash    string `json:"plan_hash,omitempty"`
}

func (f *stateFile) loadProductionRequest() (*productionRequestRecord, *failure) {
	file, err := openStateRecord(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot open the episode request record.")
	}
	defer file.Close()

	const limit = 4 << 10
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	var record productionRequestRecord
	if err != nil || len(body) > limit || json.Unmarshal(body, &record) != nil ||
		record.Version != 1 || !validHash(record.RequestHash) {
		return nil, corruptState()
	}
	if record.RunID != "" {
		if !validUUID(record.RunID) || !validUUID(record.EpisodeID) || !validHash(record.PlanHash) || record.PlanVersion == 0 {
			return nil, corruptState()
		}
	} else if record.EpisodeID != "" || record.PlanVersion != 0 || record.PlanHash != "" {
		return nil, corruptState()
	}
	return &record, nil
}

func (c *client) mutateProductionRun(ctx context.Context, o options, postPath string, payload []byte, want productionRunIdentity) (*productionRun, *failure) {
	path := productionRunsPath(want.projectID)
	id, operation := o.production.requestID, strings.TrimPrefix(o.command, "episode ")
	// A refreshed device approval for the original account uses the same
	// namespace. Site, project, operation and request ID isolate the journal.
	scope := digest([]byte(c.base.String() + "\x00" + c.runScope))
	name := digest([]byte(want.projectID + "\x00" + operation + "\x00" + id))
	file, problem := lockState(ctx, "production-requests", scope, name)
	if problem != nil {
		return nil, problem
	}
	defer file.unlock()

	record, problem := file.loadProductionRequest()
	if problem != nil {
		return nil, problem
	}
	requestHash := digest(append([]byte(postPath+"\x00"), payload...))
	const writeError = "Could not save the episode request record. Retry with the same request ID and inputs."
	if record != nil {
		if record.RequestHash != requestHash {
			return nil, fail("idempotency_conflict", "This episode request ID already belongs to different inputs. Restore the original request before continuing.")
		}
		if record.RunID != "" {
			want.runID, want.episodeID = record.RunID, record.EpisodeID
			want.planHash, want.planVersion = record.PlanHash, record.PlanVersion
			return c.readProductionRun(ctx, path, want, "")
		}
		// A pending record might represent a paid request whose reply was lost.
		lookup, problem := c.listProductionRuns(ctx, path, want, id, operation, "")
		if problem != nil {
			return nil, problem
		}
		switch lookup.LookupState {
		case "accepted":
			if lookup.Receipt.RequestHash != digest(payload) {
				return nil, fail("idempotency_conflict", "The site accepted a different request under this ID. Keep the original receipt and check the previous inputs.")
			}
			run := &lookup.Items[0]
			return run, record.saveRun(file, run, writeError)
		case "not_accepted":
			return nil, fail("episode_request_rejected", "The site confirmed this request ID was not accepted and is closed. Review the plan and budget before starting a new request.")
		case "uncertain":
			if !o.production.replay {
				return nil, fail("episode_request_uncertain", "The site cannot confirm whether this request was accepted. Check the original request, or explicitly replay its unchanged inputs with --replay if the original authorization still applies.")
			}
		}
	} else {
		if o.production.replay {
			return nil, fail("episode_request_not_saved", "No original request is saved on this device. Look up the original request ID with episode status.")
		}
		record = &productionRequestRecord{Version: 1, RequestHash: requestHash}
		if problem := file.writeJSON(record, writeError); problem != nil {
			return nil, problem
		}
	}
	statuses := []int{http.StatusOK, http.StatusCreated}
	if operation == "resume" {
		statuses = []int{http.StatusOK, http.StatusAccepted}
	}
	var run productionRun
	if problem := c.json(ctx, http.MethodPost, postPath, payload, id, true, &run, statuses...); problem != nil {
		return nil, problem
	}
	if !run.validate(want) {
		return nil, invalidProductionRunReply()
	}
	return &run, record.saveRun(file, &run, writeError)
}

func (r *productionRequestRecord) saveRun(file *stateFile, run *productionRun, message string) *failure {
	r.RunID, r.EpisodeID = run.RunID, run.EpisodeID
	r.PlanVersion, r.PlanHash = run.PlanVersion, run.PlanHash
	return file.writeJSON(r, message)
}
