package ifytvcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	deviceSecret  = "private-challenge-not-for-the-browser"
	deviceToken   = "ifytv_device_private-token-not-for-display"
	challengeJSON = `{"device_code":"` + deviceSecret + `","user_code":"BCDF-2345","expires_in":600,"interval":1,"verification_uri":"https://untrusted.invalid/signed"}`
)

func authCommand(args ...string) invocation {
	return invoke(context.Background(), append(append([]string{"auth"}, args...), "--json")...)
}

func authResult(t *testing.T, v invocation, ok bool, status string) map[string]any {
	t.Helper()
	for _, secret := range []string{deviceSecret, deviceToken, "untrusted.invalid"} {
		if strings.Contains(v.out+v.diagnostics, secret) {
			t.Fatal("device secret or untrusted URL appeared in output")
		}
	}
	result := v.result(t, ok)
	if result["status"] != status {
		t.Fatal(result)
	}
	return result
}

func expectError(t *testing.T, out map[string]any, code string) {
	t.Helper()
	if e, ok := out["error"].(map[string]any); !ok || e["code"] != code {
		t.Fatalf("expected %s, got %v", code, out)
	}
}

func deviceReply(w http.ResponseWriter, token, scope string) {
	jsonReply(w, 200, fmt.Sprintf(`{"access_token":%q,"run_scope":%q,"expires_in":86400}`, token, scope))
}

func checkDeviceRequest(t *testing.T, r *http.Request, body map[string]string, token string) {
	t.Helper()
	wantHeader := ""
	if token != "" {
		wantHeader = "Bearer " + token
	}
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Authorization") != wantHeader {
		t.Error("unexpected device request method, URL or authorization header")
	}
	if body != nil {
		var got map[string]string
		if json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, body) {
			t.Error("unexpected device request JSON")
		}
	}
}

func TestDeviceLoginIsImmediateAndProtectedCommandsShareItsLink(t *testing.T) {
	var starts atomic.Int32
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/device/cancel" {
			checkDeviceRequest(t, r, map[string]string{"device_code": deviceSecret}, "")
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != "/api/v1/auth/device/start" {
			t.Error("unexpected request before browser approval", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		checkDeviceRequest(t, r, map[string]string{}, "")
		starts.Add(1)
		jsonReply(w, 200, challengeJSON)
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("status"), true, "signed_out")
	// Separate invocations race to initialize a single site-specific challenge.
	const n = 5
	results := make(chan invocation, n)
	for i := 0; i < n; i++ {
		go func() { results <- authCommand("login") }()
	}
	for i := 0; i < n; i++ {
		out := authResult(t, <-results, true, "authorization_required")
		if out["authorization_url"] != server.URL+"/cli?device=BCDF-2345" || out["user_code"] != "BCDF-2345" || out["authorization_id"] != digest([]byte(deviceSecret)) {
			t.Fatal(out)
		}
	}
	pending := authResult(t, authCommand("status"), true, "authorization_required")
	out := authResult(t, createRun("absent.json", "original shot"), false, "authorization_required")
	expectError(t, out, "authorization_required")
	if out["user_code"] != pending["user_code"] || out["authorization_url"] != pending["authorization_url"] || out["run"] != "original shot" {
		t.Fatal(out)
	}
	plain := invoke(context.Background(), "auth", "login")
	if plain.code != 0 || !strings.Contains(plain.out, server.URL+"/cli?device=BCDF-2345") ||
		!strings.Contains(plain.out, "--authorization-id "+digest([]byte(deviceSecret))) || strings.Contains(plain.out, deviceSecret) {
		t.Fatal("interactive login must show the safe verification URL")
	}
	timed := authCommand("wait", "--timeout", "35ms")
	expectError(t, timed.result(t, false), "timeout")
	waiting := make(chan invocation, 1)
	go func() { waiting <- authCommand("wait", "--timeout", "3s") }()
	authResult(t, authCommand("logout"), true, "signed_out")
	if v := <-waiting; v.code == 0 {
		t.Fatal("wait succeeded after pending login was canceled")
	}
	authResult(t, authCommand("status"), true, "signed_out")
	if starts.Load() != 1 {
		t.Fatalf("started %d separate authorizations", starts.Load())
	}
}

func TestDeviceExchangeResumesLegacyPaidRunAndUsesBearerForProtectedRequests(t *testing.T) {
	var paid, polls, revokes atomic.Int32
	var originalKey, originalPayload string
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			if r.Header.Get("Authorization") != "" {
				t.Error("catalog included credentials")
			}
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			payload, _ := io.ReadAll(r.Body)
			if paid.Add(1) == 1 {
				if r.Header.Get("Authorization") != "Bearer "+testKey {
					t.Error("missing env key")
				}
				originalKey, originalPayload = r.Header.Get("Idempotency-Key"), string(payload)
				jsonReply(w, 502, `{"error":{"code":"gateway_timeout"}}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+deviceToken || originalKey == "" || r.Header.Get("Idempotency-Key") != originalKey || string(payload) != originalPayload {
				t.Error("device retry changed the credential scope, idempotency key or payload")
			}
			created(w)
		case "/api/v1/auth/device/start":
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			site := "http://" + r.Host
			checkDeviceRequest(t, r, map[string]string{"site": site, "device_code": deviceSecret}, "")
			if polls.Add(1) == 1 {
				jsonReply(w, 400, `{"error":{"code":"authorization_pending"}}`)
				return
			}
			deviceReply(w, deviceToken, digest([]byte(site+"\x00"+testKey)))
		case "/api/v1/tasks/" + taskID:
			if r.Header.Get("Authorization") != "Bearer "+deviceToken {
				t.Error("persisted token missing")
			}
			jsonReply(w, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded"}`, taskID))
		case "/api/v1/auth/device/revoke":
			checkDeviceRequest(t, r, nil, deviceToken)
			if revokes.Add(1) == 1 {
				jsonReply(w, 503, `{"error":{"code":"service_unavailable"}}`)
				return
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected path", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	spec := writeSpec(t, t.TempDir(), newSpec())
	createRun(spec, "scene").result(t, false)
	t.Setenv("IFYTV_API_KEY", "")
	blocked := authResult(t, createRun(spec, "scene"), false, "authorization_required")
	if blocked["authorization_url"] != server.URL+"/cli?device=BCDF-2345" || paid.Load() != 1 {
		t.Fatal(blocked)
	}
	// Both agents can wait on the same login without exchanging it twice.
	ready := make(chan invocation, 2)
	for i := 0; i < 2; i++ {
		go func() { ready <- authCommand("wait", "--timeout", "8s") }()
	}
	for i := 0; i < 2; i++ {
		if out := authResult(t, <-ready, true, "authenticated"); out["source"] != "device" {
			t.Fatal(out)
		}
	}
	if polls.Load() != 2 {
		t.Fatal("duplicate token exchanges", polls.Load())
	}
	if out := createRun(spec, "scene").result(t, true); out["task_id"] != taskID {
		t.Fatal(out)
	}
	createRun(spec, "scene").result(t, true)
	// A device bearer in the environment still identifies its original scope.
	t.Setenv("IFYTV_API_KEY", deviceToken)
	createRun(spec, "scene").result(t, true)
	t.Setenv("IFYTV_API_KEY", "")
	if out := invoke(context.Background(), "video", "status", "--run", "scene", "--json").result(t, true); out["status"] != "succeeded" || paid.Load() != 2 {
		t.Fatal(out)
	}
	authCommand("logout").result(t, false)
	authResult(t, authCommand("status"), true, "cancellation_pending")
	authResult(t, authCommand("logout"), true, "signed_out")
	authResult(t, authCommand("logout"), true, "signed_out")
	if revokes.Load() != 2 {
		t.Fatal("duplicate or missing revocation")
	}
}

func TestExplicitDeviceBearerWithoutScopeCannotStartPaidRun(t *testing.T) {
	var hits atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) })
	t.Setenv("IFYTV_API_KEY", deviceToken)
	out := createRun(writeSpec(t, t.TempDir(), newSpec()), "existing").result(t, false)
	expectError(t, out, "device_scope_required")
	if hits.Load() != 0 {
		t.Fatal("unscoped device bearer reached the API")
	}
}

func TestDeviceStatusWithEnvironmentRequiresNoStorageOrNetwork(t *testing.T) {
	var hits atomic.Int32
	testSite(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) })
	file := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IFYTV_STATE_DIR", filepath.Join(file, "state"))
	for _, cmd := range []string{"status", "login", "wait"} {
		if out := authResult(t, authCommand(cmd), true, "authenticated"); out["source"] != "environment" {
			t.Fatal(out)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("auth inspected the server despite an explicit key")
	}
}

func TestDeviceIsolatedBySiteAndRefusesUnsafeTransportOrReplies(t *testing.T) {
	var phase atomic.Int32
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1); http.NotFound(w, r) }))
	defer other.Close()
	first := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case 0:
			jsonReply(w, 200, strings.ReplaceAll(challengeJSON, "BCDF-2345", "../?token=leak"))
		case 1:
			jsonReply(w, 200, challengeJSON)
		case 2:
			deviceReply(w, deviceToken, strings.Repeat("A", 64))
		default:
			http.Redirect(w, r, other.URL+"/secrets", http.StatusTemporaryRedirect)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	expectError(t, authCommand("login").result(t, false), "invalid_response")
	phase.Store(1)
	authResult(t, authCommand("login"), true, "authorization_required")
	t.Setenv("IFYTV_BASE_URL", other.URL)
	authResult(t, authCommand("status"), true, "signed_out")
	t.Setenv("IFYTV_BASE_URL", first.URL)
	phase.Store(2)
	expectError(t, authCommand("wait", "--timeout", "4s").result(t, false), "invalid_response")
	phase.Store(3)
	expectError(t, authCommand("wait", "--timeout", "4s").result(t, false), "redirect_rejected")
	t.Setenv("IFYTV_BASE_URL", "http://192.0.2.27")
	expectError(t, authCommand("login").result(t, false), "insecure_base_url")
	if otherHits.Load() != 0 {
		t.Fatal("other site received a request")
	}
}

func TestDeviceRevocationAndReauthorizationPinOriginalAccount(t *testing.T) {
	var starts, exchanges, paid, revokes atomic.Int32
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			starts.Add(1)
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			if exchanges.Add(1) == 1 {
				deviceReply(w, deviceToken, strings.Repeat("a", 64))
				return
			}
			deviceReply(w, deviceToken+"other", strings.Repeat("b", 64))
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			paid.Add(1)
			jsonReply(w, 401, `{"error":{"code":"device_unauthorized"}}`)
		case "/api/v1/auth/device/revoke":
			token := deviceToken
			if exchanges.Load() > 1 {
				token += "other"
			}
			checkDeviceRequest(t, r, nil, token)
			revokes.Add(1)
			w.WriteHeader(204)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	authResult(t, authCommand("wait", "--timeout", "4s"), true, "authenticated")
	spec := writeSpec(t, t.TempDir(), newSpec())
	out := authResult(t, createRun(spec, "paid"), false, "authorization_required")
	expectError(t, out, "authorization_required")
	if out["run"] != "paid" || out["authorization_url"] != server.URL+"/cli?device=BCDF-2345" {
		t.Fatal(out)
	}
	expectError(t, authCommand("wait", "--timeout", "4s").result(t, false), "authorization_scope_changed")
	if starts.Load() != 2 || paid.Load() != 1 || revokes.Load() != 2 {
		t.Fatal("unexpected challenges, creates or revocations", starts.Load(), paid.Load(), revokes.Load())
	}
	authResult(t, authCommand("status"), true, "signed_out")
	authResult(t, authCommand("logout"), true, "signed_out")
	authResult(t, authCommand("login"), true, "authorization_required")
	authResult(t, authCommand("wait", "--timeout", "4s"), true, "authenticated")
}

func TestDeviceAccountSwitchWhileCreateIsInFlightRequiresAccountCheck(t *testing.T) {
	var exchanges, paid atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			if exchanges.Add(1) == 1 {
				deviceReply(w, deviceToken, strings.Repeat("a", 64))
				return
			}
			deviceReply(w, deviceToken+"replacement", strings.Repeat("b", 64))
		case "/api/v1/auth/device/revoke":
			w.WriteHeader(204)
		case "/api/v1/models":
			jsonReply(w, 200, catalogJSON)
		case "/api/v1/tasks":
			paid.Add(1)
			close(started)
			<-release
			jsonReply(w, 401, `{"error":{"code":"device_unauthorized"}}`)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	authResult(t, authCommand("wait", "--timeout", "5s"), true, "authenticated")
	spec := writeSpec(t, t.TempDir(), newSpec())
	done := make(chan invocation, 1)
	go func() { done <- createRun(spec, "under-original-account") }()
	<-started
	// The operator explicitly signs in to a different account in another terminal.
	func() {
		defer close(release)
		authResult(t, authCommand("logout"), true, "signed_out")
		authResult(t, authCommand("login"), true, "authorization_required")
		authResult(t, authCommand("wait", "--timeout", "5s"), true, "authenticated")
	}()
	expectError(t, (<-done).result(t, false), "authorization_scope_changed")
	if paid.Load() != 1 {
		t.Fatal("create automatically retried under a different account")
	}
}

func TestDeviceStaleUnauthorizedCannotRestartAfterExplicitLogout(t *testing.T) {
	var hits atomic.Int32
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		jsonReply(w, 200, challengeJSON)
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("logout"), true, "signed_out")
	// Another already-running command still has the rejected credential.
	c, problem := newClient(server.URL, "", true)
	if problem != nil {
		t.Fatal(problem)
	}
	c.key, c.runScope = deviceToken, strings.Repeat("a", 64)
	_, problem = c.login(context.Background(), output{Run: "scene"}, deviceToken, "")
	if problem == nil || problem.Code != "authorization_scope_changed" || hits.Load() != 0 {
		t.Fatal("a stale request restarted sign-in after explicit logout", problem, hits.Load())
	}
}

func TestDeviceSlowDownSurvivesWaitTimeoutAndResumesSameChallenge(t *testing.T) {
	var polls atomic.Int32
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/device/start" {
			jsonReply(w, 200, challengeJSON)
			return
		}
		if polls.Add(1) == 1 {
			jsonReply(w, 429, `{"error":{"code":"slow_down"}}`)
			return
		}
		jsonReply(w, 403, `{"error":{"code":"authorization_denied"}}`)
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	// Set a due poll directly to keep the test independent of the gateway's
	// real five-second slowdown. The retry deadline must survive process exit.
	c, problem := newClient(server.URL, "", true)
	if problem != nil {
		t.Fatal(problem)
	}
	setDue := func(checkBackoff bool) {
		s, problem := c.openSession(context.Background())
		if problem != nil {
			t.Fatal(problem)
		}
		defer s.file.unlock()
		if s.Pending == nil {
			t.Fatal("missing resumable challenge")
		}
		if checkBackoff && (s.Pending.Interval != 6 || time.Until(s.Pending.NextPoll) < 3*time.Second) {
			t.Fatal("server slowdown was not persisted")
		}
		s.Pending.NextPoll = time.Now().Add(-time.Second)
		if problem = s.save(); problem != nil {
			t.Fatal(problem)
		}
	}
	setDue(false)
	expectError(t, authCommand("wait", "--timeout", "250ms").result(t, false), "timeout")
	expectError(t, authCommand("wait", "--timeout", "100ms").result(t, false), "timeout")
	if polls.Load() != 1 {
		t.Fatal("resumed wait polled during server backoff")
	}
	setDue(true)
	expectError(t, authResult(t, authCommand("wait"), false, "signed_out"), "authorization_denied")
	expectError(t, authCommand("wait").result(t, false), "authorization_not_started")
}

func TestDeviceExpiredChallengeIsReplacedAndEnvironmentOverridesSavedLogin(t *testing.T) {
	var starts, protected, revokes atomic.Int32
	server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			starts.Add(1)
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			deviceReply(w, deviceToken, strings.Repeat("a", 64))
		case "/api/v1/tasks/" + taskID:
			protected.Add(1)
			if r.Header.Get("Authorization") != "Bearer "+testKey {
				t.Error("saved login overrode explicit key")
			}
			jsonReply(w, 401, `{"error":{"code":"invalid_api_key"}}`)
		case "/api/v1/auth/device/revoke":
			checkDeviceRequest(t, r, nil, deviceToken)
			revokes.Add(1)
			w.WriteHeader(204)
		case "/api/v1/auth/device/cancel":
			checkDeviceRequest(t, r, map[string]string{"device_code": deviceSecret}, "")
			w.WriteHeader(204)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	c, problem := newClient(server.URL, "", true)
	if problem != nil {
		t.Fatal(problem)
	}
	s, problem := c.openSession(context.Background())
	if problem != nil {
		t.Fatal(problem)
	}
	s.Pending.ExpiresAt = time.Now().Add(-time.Second)
	problem = s.save()
	s.file.unlock()
	if problem != nil {
		t.Fatal(problem)
	}
	authResult(t, authCommand("status"), true, "signed_out")
	expectError(t, authResult(t, authCommand("wait"), false, "signed_out"), "authorization_expired")
	authResult(t, authCommand("login"), true, "authorization_required")
	authResult(t, authCommand("wait", "--timeout", "5s"), true, "authenticated")
	t.Setenv("IFYTV_API_KEY", testKey)
	out := invoke(context.Background(), "video", "status", "--task", taskID, "--json").result(t, false)
	expectError(t, out, "invalid_api_key")
	if active := authResult(t, authCommand("logout"), true, "authenticated"); active["source"] != "environment" {
		t.Fatal(active)
	}
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("status"), true, "signed_out")
	if starts.Load() != 2 || protected.Load() != 1 || revokes.Load() != 1 {
		t.Fatal("unexpected login, protected or revocation calls")
	}
}

func TestDeviceCancelsLostExchangeBeforeForgettingTheOnlyRecoverySecret(t *testing.T) {
	var cancels atomic.Int32
	var remotelyIssued atomic.Bool
	testSite(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/device/start":
			jsonReply(w, 200, challengeJSON)
		case "/api/v1/auth/device/token":
			remotelyIssued.Store(true)
			// The gateway committed the exchange but its reply was truncated.
			jsonReply(w, 200, `{"access_token":`)
		case "/api/v1/auth/device/cancel":
			checkDeviceRequest(t, r, map[string]string{"device_code": deviceSecret}, "")
			if cancels.Add(1) == 1 {
				jsonReply(w, 503, `{"error":{"code":"network_error"}}`)
				return
			}
			remotelyIssued.Store(false)
			w.WriteHeader(204)
		default:
			t.Error("unexpected API", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	t.Setenv("IFYTV_API_KEY", "")
	authResult(t, authCommand("login"), true, "authorization_required")
	expectError(t, authCommand("wait", "--timeout", "3s").result(t, false), "invalid_response")
	if !remotelyIssued.Load() {
		t.Fatal("exchange did not reach gateway")
	}
	authResult(t, authCommand("logout"), false, "cancellation_pending")
	if out := authResult(t, authCommand("status"), true, "cancellation_pending"); out["authorization_url"] != nil {
		t.Fatal("canceled login still invites approval")
	}
	authResult(t, authCommand("logout"), true, "signed_out")
	authResult(t, authCommand("logout"), true, "signed_out")
	if remotelyIssued.Load() || cancels.Load() != 2 {
		t.Fatal("an issued device token survived confirmed logout")
	}
}

func TestDeviceBoundWaitAndCreateOnlyUseTheDisplayedAuthorization(t *testing.T) {
	for _, first := range []string{"denied", "approved"} {
		t.Run(first, func(t *testing.T) {
			const secondCode = "private-second-device-secret"
			var starts, exchanges, uploads, paid, catalogs atomic.Int32
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/device/start":
					if starts.Add(1) == 1 {
						jsonReply(w, 200, challengeJSON)
						return
					}
					jsonReply(w, 200, strings.ReplaceAll(strings.ReplaceAll(challengeJSON, deviceSecret, secondCode), "BCDF-2345", "RSTV-6789"))
				case "/api/v1/auth/device/token":
					if exchanges.Add(1) == 1 {
						if first == "denied" {
							jsonReply(w, 403, `{"error":{"code":"authorization_denied"}}`)
							return
						}
						deviceReply(w, deviceToken, strings.Repeat("a", 64))
						return
					}
					deviceReply(w, deviceToken+"second", strings.Repeat("b", 64))
				case "/api/v1/auth/device/revoke":
					w.WriteHeader(204)
				case "/api/v1/models":
					catalogs.Add(1)
					jsonReply(w, 200, catalogJSON)
				case "/api/v1/tasks":
					paid.Add(1)
					created(w)
				case "/api/v1/uploads":
					uploads.Add(1)
					http.NotFound(w, r)
				default:
					t.Error(r.URL.Path)
					http.NotFound(w, r)
				}
			})
			t.Setenv("IFYTV_API_KEY", "")
			idA, idB := digest([]byte(deviceSecret)), digest([]byte(secondCode))
			authResult(t, authCommand("login"), true, "authorization_required")
			if first == "denied" {
				expectError(t, authCommand("wait", "--authorization-id", idA, "--timeout", "4s").result(t, false), "authorization_denied")
			} else {
				if actual := authResult(t, authCommand("wait", "--authorization-id", idA, "--timeout", "4s"), true, "authenticated")["authorization_id"]; actual != idA {
					t.Fatal("wrong device identity", actual)
				}
				authResult(t, authCommand("logout"), true, "signed_out")
			}
			authResult(t, authCommand("login"), true, "authorization_required")
			expectError(t, authCommand("wait", "--authorization-id", idA).result(t, false), "authorization_changed")
			authResult(t, authCommand("wait", "--authorization-id", idB, "--timeout", "4s"), true, "authenticated")
			expectError(t, authCommand("wait", "--authorization-id", idA).result(t, false), "authorization_changed")
			dir := t.TempDir()
			localImage(t, dir, "asset.png", "test")
			withImage := newSpec()
			withImage["images"] = []string{"asset.png"}
			path := writeSpec(t, dir, withImage)
			bound := func(id string) invocation {
				return invoke(context.Background(), "video", "create", "--no-canvas", "--spec", path, "--run", "same-run", "--authorization-id", id, "--json")
			}
			expectError(t, bound(idA).result(t, false), "authorization_changed")
			t.Setenv("IFYTV_API_KEY", testKey)
			expectError(t, bound(idB).result(t, false), "authorization_changed")
			expectError(t, authCommand("wait", "--authorization-id", idB).result(t, false), "authorization_changed")
			t.Setenv("IFYTV_API_KEY", "")
			if catalogs.Load() != 0 || uploads.Load() != 0 || paid.Load() != 0 {
				t.Fatal("a mismatched approval made API calls")
			}
			path = writeSpec(t, dir, newSpec())
			if got := bound(idB).result(t, true); got["task_id"] != taskID || paid.Load() != 1 {
				t.Fatal(got)
			}
			expectError(t, bound("").result(t, false), "usage")
		})
	}
}

func TestExpiredDeviceSecretIsRetainedUntilRemoteCleanupSucceeds(t *testing.T) {
	for _, operation := range []string{"wait", "login", "server expiry"} {
		t.Run(operation, func(t *testing.T) {
			var cancels, starts atomic.Int32
			server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/device/start":
					starts.Add(1)
					jsonReply(w, 200, challengeJSON)
				case "/api/v1/auth/device/token":
					jsonReply(w, 410, `{"error":{"code":"authorization_expired"}}`)
				case "/api/v1/auth/device/cancel":
					if cancels.Add(1) == 1 {
						jsonReply(w, 503, `{"error":{"code":"unavailable"}}`)
						return
					}
					w.WriteHeader(204)
				default:
					t.Error(r.URL.Path)
					http.NotFound(w, r)
				}
			})
			t.Setenv("IFYTV_API_KEY", "")
			authResult(t, authCommand("login"), true, "authorization_required")
			c, _ := newClient(server.URL, "", true)
			s, problem := c.openSession(context.Background())
			if problem != nil {
				t.Fatal(problem)
			}
			if operation == "server expiry" {
				s.Pending.NextPoll = time.Now().Add(-time.Second)
			} else {
				s.Pending.ExpiresAt = time.Now().Add(-time.Second)
			}
			problem = s.save()
			s.file.unlock()
			if problem != nil {
				t.Fatal(problem)
			}
			cmd := operation
			if cmd == "server expiry" {
				cmd = "wait"
			}
			expectError(t, authCommand(cmd).result(t, false), "unavailable")
			if starts.Load() != 1 {
				t.Fatal("lost the original recovery secret while cancel was unavailable")
			}
			authResult(t, authCommand("logout"), true, "signed_out")
			if cancels.Load() != 2 {
				t.Fatal("logout did not retry remote cleanup")
			}
		})
	}
}

func TestDeviceUnusableBearerRequiresConfirmedRevocationBeforeReplacement(t *testing.T) {
	for _, cause := range []string{"local expiry", "protected 401"} {
		t.Run(cause, func(t *testing.T) {
			var revokes, starts atomic.Int32
			server := testSite(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/auth/device/start":
					starts.Add(1)
					jsonReply(w, 200, challengeJSON)
				case "/api/v1/auth/device/token":
					deviceReply(w, deviceToken, strings.Repeat("a", 64))
				case "/api/v1/tasks/" + taskID:
					jsonReply(w, 401, `{"error":{"code":"device_unauthorized"}}`)
				case "/api/v1/auth/device/revoke":
					checkDeviceRequest(t, r, nil, deviceToken)
					if revokes.Add(1) == 1 {
						if cause == "local expiry" {
							w.Header().Set("Content-Type", "text/html")
							_, _ = io.WriteString(w, "<html>old release</html>")
						} else {
							jsonReply(w, 503, `{"error":{"code":"unavailable"}}`)
						}
						return
					}
					w.WriteHeader(204)
				default:
					t.Error(r.URL.Path)
					http.NotFound(w, r)
				}
			})
			t.Setenv("IFYTV_API_KEY", "")
			authResult(t, authCommand("login"), true, "authorization_required")
			authResult(t, authCommand("wait", "--timeout", "5s"), true, "authenticated")
			if cause == "local expiry" {
				c, _ := newClient(server.URL, "", true)
				s, problem := c.openSession(context.Background())
				if problem != nil {
					t.Fatal(problem)
				}
				s.Credential.ExpiresAt = time.Now().Add(-time.Second)
				problem = s.save()
				s.file.unlock()
				if problem != nil {
					t.Fatal(problem)
				}
				authResult(t, authCommand("login"), false, "cancellation_pending")
			} else {
				authResult(t, invoke(context.Background(), "video", "status", "--task", taskID, "--json"), false, "cancellation_pending")
			}
			authResult(t, authCommand("status"), true, "cancellation_pending")
			if starts.Load() != 1 || revokes.Load() != 1 {
				t.Fatal("replaced a token without confirmed remote revocation")
			}
			authResult(t, authCommand("logout"), true, "signed_out")
			if revokes.Load() != 2 {
				t.Fatal("lost the previous bearer")
			}
		})
	}
}
