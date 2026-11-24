package ifytvcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

const deviceTokenPrefix = "ifytv_device_"

var userCodePattern = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)
var opaqueCodePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

func validOpaqueCode(value string) bool {
	return len(value) <= 4096 && opaqueCodePattern.MatchString(value)
}

func validDeviceToken(value string) bool {
	suffix, ok := strings.CutPrefix(value, deviceTokenPrefix)
	return ok && len(value) <= 4096 && validOpaqueCode(suffix)
}

func validRunScope(scope string) bool {
	return validHash(scope) && strings.ToLower(scope) == scope
}

const maxDurationSeconds = math.MaxInt64 / int64(time.Second)

func validSeconds(n int64) bool { return n > 0 && n <= maxDurationSeconds }

type deviceChallenge struct {
	Code      string    `json:"device_code"`
	UserCode  string    `json:"user_code"`
	ExpiresAt time.Time `json:"expires_at"`
	Interval  int64     `json:"interval"`
	NextPoll  time.Time `json:"next_poll"`
}

type deviceCredential struct {
	Token     string    `json:"access_token"`
	ExpiresAt time.Time `json:"expires_at"`
	LoginID   string    `json:"login_id"`
}

type deviceSession struct {
	file       *stateFile
	Site       string            `json:"site"`
	RunScope   string            `json:"run_scope,omitempty"`
	Pending    *deviceChallenge  `json:"pending,omitempty"`
	Credential *deviceCredential `json:"credential,omitempty"`
	Cleanup    string            `json:"pending_cleanup,omitempty"`
}

func (s *deviceSession) valid(site string) bool {
	if s.Site != site || (s.RunScope != "" && !validRunScope(s.RunScope)) {
		return false
	}
	if s.Cleanup != "" && (s.Pending == nil && s.Credential == nil ||
		(s.Cleanup != "logout" && s.Cleanup != "expired" && s.Cleanup != "renewal" && s.Cleanup != "scope_changed")) {
		return false
	}
	if p := s.Pending; p != nil {
		return s.Credential == nil && validOpaqueCode(p.Code) && p.Code != p.UserCode &&
			userCodePattern.MatchString(p.UserCode) && !p.ExpiresAt.IsZero() && validSeconds(p.Interval) && !p.NextPoll.IsZero()
	}
	if g := s.Credential; g != nil {
		return s.RunScope != "" && validDeviceToken(g.Token) && !g.ExpiresAt.IsZero() && validRunScope(g.LoginID)
	}
	return true
}

func (c *client) rememberSecret(value string) {
	if value != "" && !slices.Contains(c.secrets, value) {
		c.secrets = append(c.secrets, value)
	}
}

// The per-site kernel lock is held only for a state update or one bounded
// HTTP exchange. Other CLI processes can inspect or cancel while wait sleeps.
func (c *client) openSession(ctx context.Context) (*deviceSession, *failure) {
	site := c.base.String()
	lock, problem := lockState(ctx, "auth", digest([]byte(site)), "session")
	if problem != nil {
		return nil, problem
	}
	s := &deviceSession{file: lock, Site: site}
	f, err := openDeviceRecord(lock.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		lock.unlock()
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot read this site's saved sign-in.")
	}
	defer func() { _ = f.Close() }()
	const maxSessionBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	// Start with an empty site so a missing site field cannot use this login.
	s.Site = ""
	if err != nil || len(data) > maxSessionBytes || json.Unmarshal(data, s) != nil || !s.valid(site) {
		lock.unlock()
		return nil, fail("invalid_auth_state", "The saved sign-in is damaged. Restore its private state file or use a fresh IFYTV_STATE_DIR.")
	}
	if s.Pending != nil {
		c.rememberSecret(s.Pending.Code)
	}
	if s.Credential != nil {
		c.rememberSecret(s.Credential.Token)
	}
	return s, nil
}

func (s *deviceSession) save() *failure {
	return s.file.writeJSON(s, "Could not save the device sign-in. Check the private state directory and retry.")
}

func (s *deviceSession) describe(result output) output {
	result.Status, result.Source = "signed_out", ""
	result.UserCode, result.AuthorizationURL, result.AuthorizationID = "", "", ""
	now := time.Now()
	if s.Cleanup != "" {
		result.Status = "cancellation_pending"
		if s.Pending != nil {
			result.AuthorizationID = digest([]byte(s.Pending.Code))
		} else {
			result.AuthorizationID = s.Credential.LoginID
		}
	} else if g := s.Credential; g != nil && now.Before(g.ExpiresAt) {
		result.Status, result.Source = "authenticated", "device"
		result.AuthorizationID = g.LoginID
	} else if p := s.Pending; p != nil && now.Before(p.ExpiresAt) {
		result.Status = "authorization_required"
		result.AuthorizationID = digest([]byte(p.Code))
		result.UserCode = p.UserCode
		result.AuthorizationURL = s.Site + "/cli?device=" + p.UserCode
	}
	return result
}

func environmentStatus() output {
	return output{Task: Task{Status: "authenticated"}, Source: "environment"}
}
