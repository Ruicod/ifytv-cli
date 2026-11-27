package ifytvcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	cloudEpisodeID      = "bc85e1de-b478-49ec-8aa9-5dd3c9de833b"
	cloudEpisodeRunID   = "c90c30e9-0aa7-429b-973c-91c83e88bdde"
	cloudEpisodeRequest = "9b74ea71-ca99-44f6-91f4-396d65487709"
	cloudEpisodeQuoteID = "281f2337-4d06-4204-90ca-ad0267a57f8e"
	cloudEpisodeHash    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cloudEpisodePath    = "/api/v1/cli/projects/" + projectID + "/production-runs"
	cloudEpisodeRev     = uint64(18014398509481985)
)

func cloudEpisodeTestSite(t *testing.T, handle http.HandlerFunc) string {
	t.Helper()
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			deviceReply(w, deviceToken, productionTestScope)
		default:
			if r.Header.Get("Authorization") != "Bearer "+deviceToken {
				t.Error("production request did not use the pinned device identity")
			}
			handle(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	result := authResult(t, authCommand("wait"), true, "authenticated")
	return result["authorization_id"].(string)
}

func cloudRunFixture(status, nextAction string) string {
	return fmt.Sprintf(
		`{"project_id":%q,"run_id":%q,"episode_id":%q,"plan_version":1,"plan_hash":%q,"revision":%d,"status":%q,"next_action":{"type":%q},"steps":[],"billing":{}}`,
		projectID, cloudEpisodeRunID, cloudEpisodeID, cloudEpisodeHash, cloudEpisodeRev+1, status, nextAction)
}

func TestCloudEpisodePlanRecoversAcceptedRequestWithoutAnotherPost(t *testing.T) {
	var posts, lookups, reads atomic.Int32
	var wireHash atomic.Value
	pin := cloudEpisodeTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == cloudEpisodePath:
			posts.Add(1)
			var body struct {
				ExpectedRevision uint64 `json:"expected_revision"`
				Plan             struct {
					EpisodeID string `json:"episode_id"`
				} `json:"plan"`
			}
			wire, err := io.ReadAll(r.Body)
			wireHash.Store(digest(wire))
			if err != nil || json.Unmarshal(wire, &body) != nil || body.ExpectedRevision != cloudEpisodeRev ||
				body.Plan.EpisodeID != cloudEpisodeID || r.Header.Get("Idempotency-Key") != cloudEpisodeRequest {
				t.Error("invalid draft request")
			}
			jsonReply(w, http.StatusServiceUnavailable, `{"error":{"code":"network_busy"}}`)
		case r.Method == http.MethodGet && r.URL.Path == cloudEpisodePath:
			lookups.Add(1)
			q := r.URL.Query()
			if q.Get("request_id") != cloudEpisodeRequest || q.Get("operation") != "plan" {
				t.Error("missing original request selector")
			}
			jsonReply(w, 200, acceptedProductionLookup("plan", wireHash.Load().(string), cloudRunFixture("planned", "execute")))
		case r.Method == http.MethodGet && r.URL.Path == cloudEpisodePath+"/"+cloudEpisodeRunID:
			reads.Add(1)
			jsonReply(w, 200, cloudRunFixture("planned", "execute"))
		default:
			t.Error("unexpected episode API:", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	plan := map[string]any{"episode_id": cloudEpisodeID, "plan_version": 1, "ratio": "9:16",
		"scenes": []any{}, "entities": []any{}, "shots": []any{}, "steps": []any{}}
	path := writeSpec(t, t.TempDir(), plan)
	args := []string{"episode", "plan", "--project", projectID, "--revision", strconv.FormatUint(cloudEpisodeRev, 10),
		"--spec", path, "--request-id", cloudEpisodeRequest, "--authorization-id", pin, "--json"}
	first := invoke(context.Background(), args...).result(t, false)
	expectError(t, first, "network_busy")
	if first["request_id"] != cloudEpisodeRequest {
		t.Fatal("unconfirmed operation lost its request ID", first)
	}
	for range 2 {
		reply := invoke(context.Background(), args...).result(t, true)
		run, ok := reply["episode_run"].(map[string]any)
		if !ok || run["run_id"] != cloudEpisodeRunID || run["plan_hash"] != cloudEpisodeHash {
			t.Fatal(reply)
		}
	}
	plan["ratio"] = "16:9"
	writeSpec(t, filepath.Dir(path), plan)
	expectError(t, invoke(context.Background(), args...).result(t, false), "idempotency_conflict")
	if posts.Load() != 1 || lookups.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("operation repeated or did not recover: plans=%d lookups=%d reads=%d", posts.Load(), lookups.Load(), reads.Load())
	}
}

func TestCloudEpisodeResumePreservesPendingKeyAndZeroBudget(t *testing.T) {
	var posts, lookups atomic.Int32
	pin := cloudEpisodeTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == cloudEpisodePath+"/"+cloudEpisodeRunID+"/resume":
			attempt := posts.Add(1)
			var body struct {
				PlanHash string `json:"plan_hash"`
				QuoteID  string `json:"quote_id"`
				Budget   struct {
					Currency string `json:"currency"`
					MaxUnits *int64 `json:"max_total_units"`
				} `json:"budget"`
				StepIDs []string `json:"step_ids"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.PlanHash != cloudEpisodeHash ||
				body.QuoteID != cloudEpisodeQuoteID || body.Budget.Currency != "credits" ||
				body.Budget.MaxUnits == nil || *body.Budget.MaxUnits != 0 || len(body.StepIDs) != 1 ||
				body.StepIDs[0] != "shot01-video" || r.Header.Get("Idempotency-Key") != cloudEpisodeRequest {
				t.Error("resume request dropped its selection or budget")
			}
			if attempt == 1 {
				jsonReply(w, 503, `{"error":{"code":"temporarily_unavailable"}}`)
			} else {
				jsonReply(w, 202, cloudRunFixture("running", "poll"))
			}
		case r.Method == http.MethodGet && r.URL.Path == cloudEpisodePath:
			lookups.Add(1)
			if r.URL.Query().Get("request_id") != cloudEpisodeRequest || r.URL.Query().Get("operation") != "resume" {
				t.Error("lookup must use the exact original key and operation")
			}
			jsonReply(w, 200, `{"lookup_state":"uncertain","items":[]}`)
		default:
			t.Error("unexpected API:", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	args := []string{"episode", "resume", "--project", projectID, "--run-id", cloudEpisodeRunID,
		"--plan-hash", cloudEpisodeHash, "--revision", "2", "--request-id", cloudEpisodeRequest,
		"--quote-id", cloudEpisodeQuoteID, "--budget-units", "0", "--currency", "credits",
		"--step-id", "shot01-video", "--authorization-id", pin, "--json"}
	expectError(t, invoke(context.Background(), args...).result(t, false), "temporarily_unavailable")
	uncertain := invoke(context.Background(), args...).result(t, false)
	expectError(t, uncertain, "episode_request_uncertain")
	if uncertain["request_id"] != cloudEpisodeRequest || posts.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("unknown request was lost or resubmitted: %v posts=%d lookups=%d", uncertain, posts.Load(), lookups.Load())
	}
	replay := invoke(context.Background(), append(args, "--replay")...).result(t, true)
	if replay["status"] != "running" || posts.Load() != 2 || lookups.Load() != 2 {
		t.Fatalf("explicit replay did not check and submit the original request: %v posts=%d lookups=%d", replay, posts.Load(), lookups.Load())
	}
}

func acceptedProductionLookup(operation, hash, run string) string {
	return fmt.Sprintf(`{"lookup_state":"accepted","items":[%s],"receipt":{"request_id":%q,"operation":%q,"run_id":%q,"request_sha256":%q}}`,
		run, cloudEpisodeRequest, operation, cloudEpisodeRunID, hash)
}

func TestCloudEpisodeRejectsLookupForDifferentInputs(t *testing.T) {
	var posts, lookups atomic.Int32
	pin := cloudEpisodeTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			jsonReply(w, 409, `{"error":{"code":"idempotency_conflict"}}`)
		case http.MethodGet:
			lookups.Add(1)
			jsonReply(w, 200, acceptedProductionLookup("resume", strings.Repeat("b", 64), cloudRunFixture("running", "poll")))
		}
	})
	args := []string{"episode", "resume", "--project", projectID, "--run-id", cloudEpisodeRunID,
		"--plan-hash", cloudEpisodeHash, "--revision", "2", "--request-id", cloudEpisodeRequest,
		"--quote-id", cloudEpisodeQuoteID, "--budget-units", "1", "--currency", "credits", "--authorization-id", pin, "--json"}
	expectError(t, invoke(context.Background(), args...).result(t, false), "idempotency_conflict")
	out := invoke(context.Background(), append(args, "--replay")...).result(t, false)
	expectError(t, out, "idempotency_conflict")
	if out["episode_run"] != nil || posts.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("different operation was adopted or submitted: %v", out)
	}
}

func TestCloudEpisodeStatusFollowsOpaqueRunCursor(t *testing.T) {
	const cursor = "second/+1=="
	pin := cloudEpisodeTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != cloudEpisodePath+"/"+cloudEpisodeRunID || r.URL.Query().Get("cursor") != cursor {
			t.Error("incorrect run detail cursor request", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		reply := cloudRunFixture("running", "poll")
		reply = strings.ReplaceAll(reply, cloudEpisodeRunID, strings.ToUpper(cloudEpisodeRunID))
		jsonReply(w, 200, reply)
	})
	out := invoke(context.Background(), "episode", "status", "--project", projectID, "--run-id", cloudEpisodeRunID,
		"--cursor", cursor, "--authorization-id", pin, "--json").result(t, true)
	if out["episode_run"].(map[string]any)["run_id"] != cloudEpisodeRunID {
		t.Fatal("run UUID was not normalized", out)
	}
}

func TestCloudEpisodeStatusDistinguishesRenderedAssetFromPublishedCard(t *testing.T) {
	var read atomic.Int32
	pin := cloudEpisodeTestSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != cloudEpisodePath+"/"+cloudEpisodeRunID {
			t.Error("unexpected request", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if read.Add(1) == 1 {
			reply := strings.TrimSuffix(cloudRunFixture("blocked", "review"), "}")
			jsonReply(w, 200, reply+fmt.Sprintf(`,"artifact":{"asset_id":%q}}`, assetID))
			return
		}
		reply := strings.TrimSuffix(cloudRunFixture("succeeded", "done"), "}")
		jsonReply(w, 200, reply+fmt.Sprintf(`,"artifact":{"asset_id":%q,"node_id":"episode-final"}}`, assetID))
	})
	args := []string{"episode", "status", "--project", projectID, "--run-id", cloudEpisodeRunID,
		"--authorization-id", pin, "--json"}
	blocked := invoke(context.Background(), args...).result(t, true)
	if blocked["status"] != "blocked" || blocked["node_id"] != nil || blocked["asset_id"] != nil {
		t.Fatalf("rendered output was reported as a published card: %v", blocked)
	}
	published := invoke(context.Background(), args...).result(t, true)
	if published["status"] != "succeeded" || published["asset_id"] != assetID || published["node_id"] != "episode-final" ||
		!strings.HasSuffix(published["project_url"].(string), "?node=episode-final") {
		t.Fatalf("missing published card: %v", published)
	}
}
