package ifytvcli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEpisodeQuoteUsesPinnedProjectAndPlan(t *testing.T) {
	const runID = "9b47a810-9b2a-4375-88db-c820df9514dc"
	const quoteID = "5175f121-97de-4a52-84fe-1527b0f0f566"
	const quotePath = "/api/v1/cli/projects/" + projectID + "/production-runs/" + runID + "/quote"
	planHash := strings.Repeat("a", 64)
	expiresAt := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339)

	for _, tc := range []struct {
		name      string
		stepID    string
		wrongHash bool
		redirect  bool
		wantError string
	}{
		{name: "selected step", stepID: "shot-01.video"},
		{name: "unknown total"},
		{name: "wrong plan", wrongHash: true, wantError: "invalid_response"},
		{name: "cross-origin redirect", redirect: true, wantError: "redirect_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits, redirects atomic.Int32
			foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirects.Add(1)
				http.NotFound(w, r)
			}))
			defer foreign.Close()

			wantRequest := map[string]any{"plan_hash": planHash}
			quote := map[string]any{
				"quote_id": quoteID, "plan_hash": planHash, "currency": "CNY", "expires_at": expiresAt,
				"lines": []any{map[string]any{"step_id": "shot-01.video", "estimate_kind": "unknown"}},
			}
			if tc.stepID != "" {
				wantRequest["step_ids"] = []any{tc.stepID}
				quote["lines"] = []any{map[string]any{
					"step_id": tc.stepID, "estimate_kind": "upper_bound", "max_units": float64(12),
				}}
				quote["max_total_units"] = float64(12)
			}
			if tc.wrongHash {
				quote["plan_hash"] = strings.Repeat("b", 64)
			}
			reply, err := json.Marshal(quote)
			if err != nil {
				t.Fatal(err)
			}
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/device/start":
					checkDeviceRequest(t, r, map[string]string{}, "")
					jsonReply(w, http.StatusOK, challengeJSON)
				case "/api/v1/auth/device/token":
					checkDeviceRequest(t, r, map[string]string{"site": "http://" + r.Host, "device_code": deviceSecret}, "")
					deviceReply(w, deviceToken, productionTestScope)
				case quotePath:
					hits.Add(1)
					if r.Method != http.MethodPost || r.URL.RawQuery != "" ||
						r.Header.Get("Authorization") != "Bearer "+deviceToken ||
						r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("unexpected quote request: %s %s or incorrect headers", r.Method, r.URL)
					}
					var got map[string]any
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, wantRequest) {
						t.Errorf("quote payload: got %v (%v), want %v", got, err, wantRequest)
					}
					if tc.redirect {
						http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
						return
					}
					jsonReply(w, http.StatusOK, string(reply))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			t.Setenv("IFYTV_API_KEY", "")
			login := authResult(t, authCommand("login"), true, "authorization_required")
			pin := digest([]byte(deviceSecret))
			if login["authorization_id"] != pin {
				t.Fatal("login did not identify the displayed approval")
			}
			authResult(t, authCommand("wait", "--authorization-id", pin, "--timeout", "5s"), true, "authenticated")

			args := []string{"episode", "quote", "--project", projectID, "--run-id", runID,
				"--plan-hash", planHash, "--authorization-id", pin, "--json"}
			if tc.stepID != "" {
				args = append(args, "--step-id", tc.stepID)
			}
			execution := invoke(context.Background(), args...)
			result := execution.result(t, tc.wantError == "")
			if hits.Load() != 1 || redirects.Load() != 0 || strings.Contains(execution.out+execution.diagnostics, deviceToken) {
				t.Fatal("quote used an unexpected request count or exposed a credential")
			}
			if tc.wantError != "" {
				expectError(t, result, tc.wantError)
				if result["episode_quote"] != nil {
					t.Fatal("failed quote exposed a receipt", result)
				}
				return
			}
			if result["authorization_id"] != pin || !reflect.DeepEqual(result["episode_quote"], quote) {
				t.Fatalf("unexpected quote receipt %v", result)
			}
		})
	}
}
