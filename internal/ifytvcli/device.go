package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func (c *client) runCommand(ctx context.Context, o options, original output) (output, *failure) {
	if o.authorizationID != "" && c.key != "" {
		return original, authorizationChanged()
	}
	if (o.command == "auth wait" || o.command == "video wait") && o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	switch o.command {
	case "models":
		return c.execute(ctx, o)
	case "auth logout":
		return c.logout(ctx)
	case "auth status", "auth wait", "auth login":
		if c.key != "" {
			return environmentStatus(), nil
		}
		switch o.command {
		case "auth wait":
			return c.waitForDevice(ctx, o.authorizationID)
		case "auth login":
			return c.login(ctx, original, "", "")
		default:
			s, problem := c.openSession(ctx)
			if problem != nil {
				return original, problem
			}
			defer s.file.unlock()
			return s.describe(original), nil
		}
	}
	// A video API key keeps its existing scope and never consults device state,
	// even if the environment credential is rejected.
	if c.key != "" {
		if o.run != "" && c.runScope == "" {
			// The reserved device prefix also identifies a bearer set explicitly
			// by an advanced caller. Hashing it would submit an existing run anew.
			s, problem := c.openSession(ctx)
			if problem != nil {
				return original, problem
			}
			if g := s.Credential; g != nil && s.Cleanup == "" && g.Token == c.key {
				c.runScope = s.RunScope
			}
			s.file.unlock()
			if c.runScope == "" {
				return original, fail("device_scope_required", "This device bearer has no saved account identity. Unset IFYTV_API_KEY and sign in with ifytv auth login, or set your original video API key.")
			}
		}
		return c.execute(ctx, o)
	}
	auth, problem := c.login(ctx, original, "", o.authorizationID)
	if problem != nil {
		return auth, problem
	}
	if auth.Status != "authenticated" {
		return auth, authorizationRequired()
	}
	result, problem := c.execute(ctx, o)
	result.AuthorizationID = auth.AuthorizationID
	if problem == nil || !problem.invalidDevice {
		return result, problem
	}
	auth, problem = c.login(ctx, result, c.key, o.authorizationID)
	if problem != nil {
		return auth, problem
	}
	if auth.Status == "authenticated" {
		// A concurrent process installed a fresh credential while this command ran.
		return auth, fail("authorization_updated", "The sign-in was renewed. Retry the same command.")
	}
	return auth, authorizationRequired()
}

func authorizationRequired() *failure {
	return fail("authorization_required", "Open the authorization URL and confirm the code. Use the returned authorization ID when running ifytv auth wait and retrying this command.")
}

func authorizationChanged() *failure {
	return fail("authorization_changed", "This command requires the exact device approval originally displayed. Check the sign-in for this request before continuing.")
}

func (c *client) devicePost(ctx context.Context, action string, payload []byte, token string, reply any) *failure {
	// Bound the time spent holding a site's state lock on an unavailable gateway.
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, problem := c.request(requestCtx, http.MethodPost, "/api/v1/auth/device/"+action, bytes.NewReader(payload), false)
	if problem != nil {
		return problem
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, problem := c.send(req)
	if problem != nil {
		if ctx.Err() == nil && requestCtx.Err() == context.DeadlineExceeded {
			return fail("network_error", "The site did not respond to the device sign-in request in time.")
		}
		return problem
	}
	defer func() { _ = resp.Body.Close() }()
	if reply == nil {
		if resp.StatusCode != http.StatusNoContent {
			return badDeviceReply()
		}
		var extra [1]byte
		if n, err := resp.Body.Read(extra[:]); n != 0 || err != io.EOF {
			return badDeviceReply()
		}
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return badDeviceReply()
	}
	return readJSON(resp.Body, reply)
}

func badDeviceReply() *failure {
	return fail("invalid_response", "The site returned an invalid device sign-in response.")
}

func (c *client) login(ctx context.Context, result output, invalidToken, boundID string) (output, *failure) {
	s, problem := c.openSession(ctx)
	if problem != nil {
		return result, problem
	}
	defer s.file.unlock()
	current := s.describe(result)
	if boundID != "" {
		if current.Status != "authenticated" || current.AuthorizationID != boundID {
			return result, authorizationChanged()
		}
		if invalidToken != "" {
			if s.Credential.Token == invalidToken {
				if problem := c.clearAuthorization(ctx, s, "renewal"); problem != nil {
					return s.describe(result), problem
				}
			}
			return result, authorizationChanged()
		}
		c.key, c.runScope = s.Credential.Token, s.RunScope
		return current, nil
	}
	if invalidToken != "" && (c.runScope != s.RunScope || s.Cleanup == "logout") {
		return result, fail("authorization_scope_changed", "The original account was signed out while this command was running. Sign in with that account to resume this run.")
	}
	if g := s.Credential; g != nil {
		if g.Token != invalidToken && current.Status == "authenticated" {
			c.key, c.runScope = g.Token, s.RunScope
			return current, nil
		}
		// Keep the previous run scope across expiry and revocation. Reapproval
		// must select this account to resume an interrupted billable operation.
		if problem := c.clearAuthorization(ctx, s, "renewal"); problem != nil {
			return s.describe(result), problem
		}
	}
	if current.Status == "authorization_required" {
		return current, nil
	}
	if s.Pending != nil {
		if problem := c.clearAuthorization(ctx, s, "expired"); problem != nil {
			return s.describe(result), problem
		}
	}
	var start struct {
		Code      string `json:"device_code"`
		UserCode  string `json:"user_code"`
		ExpiresIn int64  `json:"expires_in"`
		Interval  int64  `json:"interval"`
	}
	if problem := c.devicePost(ctx, "start", []byte("{}"), "", &start); problem != nil {
		return result, problem
	}
	if !validOpaqueCode(start.Code) || start.Code == start.UserCode || !userCodePattern.MatchString(start.UserCode) ||
		!validSeconds(start.ExpiresIn) || !validSeconds(start.Interval) {
		return result, badDeviceReply()
	}
	c.rememberSecret(start.Code)
	now := time.Now()
	s.Pending = &deviceChallenge{Code: start.Code, UserCode: start.UserCode, ExpiresAt: now.Add(time.Duration(start.ExpiresIn) * time.Second),
		Interval: start.Interval, NextPoll: now.Add(time.Duration(start.Interval) * time.Second)}
	if problem := s.save(); problem != nil {
		return result, problem
	}
	return s.describe(result), nil
}

func (c *client) revoke(ctx context.Context, token string) *failure {
	problem := c.devicePost(ctx, "revoke", []byte("{}"), token, nil)
	if problem != nil && problem.HTTPStatus == http.StatusUnauthorized && problem.Code == "device_unauthorized" {
		return nil // Already revoked or expired on the same site.
	}
	return problem
}

// A lost exchange reply leaves only the challenge on disk. An expired local
// clock or disabled remote account can also hide an active grant. Persist the
// appropriate recovery secret and cleanup intent until the gateway confirms.
func (c *client) clearAuthorization(ctx context.Context, s *deviceSession, reason string) *failure {
	if s.Cleanup == "" || (reason == "logout" && s.Cleanup != reason) {
		s.Cleanup = reason
		if problem := s.save(); problem != nil {
			return problem
		}
	}
	if p := s.Pending; p != nil {
		payload, _ := json.Marshal(struct {
			Code string `json:"device_code"`
		}{p.Code})
		if problem := c.devicePost(ctx, "cancel", payload, "", nil); problem != nil {
			return problem
		}
	} else if problem := c.revoke(ctx, s.Credential.Token); problem != nil {
		return problem
	}
	if s.Cleanup == "logout" {
		s.RunScope = ""
	}
	s.Pending, s.Credential, s.Cleanup = nil, nil, ""
	return s.save()
}

func (c *client) logout(ctx context.Context) (output, *failure) {
	s, problem := c.openSession(ctx)
	if problem != nil {
		return output{}, problem
	}
	defer s.file.unlock()
	if s.Pending != nil || s.Credential != nil {
		if problem := c.clearAuthorization(ctx, s, "logout"); problem != nil {
			return s.describe(output{}), problem
		}
	}
	if s.RunScope != "" {
		s.RunScope = ""
		if problem := s.save(); problem != nil {
			return output{}, problem
		}
	}
	if c.key != "" {
		return environmentStatus(), nil
	}
	return s.describe(output{}), nil
}

func (c *client) waitForDevice(ctx context.Context, boundID string) (output, *failure) {
	var expected string
	for {
		result, next, problem := c.pollDevice(ctx, &expected, boundID)
		if problem == nil && !next.IsZero() {
			problem = pause(ctx, time.Until(next))
			if problem == nil {
				continue
			}
		}
		if problem != nil && ctx.Err() != nil {
			problem = contextFailure(ctx)
			problem.Message = "Device sign-in wait interrupted. Repeat the same wait command to resume this approval."
		}
		return result, problem
	}
}

func (c *client) pollDevice(ctx context.Context, expected *string, boundID string) (output, time.Time, *failure) {
	var done time.Time
	s, problem := c.openSession(ctx)
	if problem != nil {
		return output{}, done, problem
	}
	defer s.file.unlock()
	result := s.describe(output{})
	if boundID != "" && result.AuthorizationID != boundID && (s.Pending == nil || digest([]byte(s.Pending.Code)) != boundID) {
		return output{}, done, authorizationChanged()
	}
	if result.Status == "authenticated" {
		if *expected != "" && s.Credential.LoginID != digest([]byte(*expected)) {
			return result, done, fail("authorization_replaced", "A different sign-in completed while waiting. Check the active account before continuing.")
		}
		return result, done, nil
	}
	p := s.Pending
	if p == nil {
		if s.Cleanup != "" {
			if problem := c.clearAuthorization(ctx, s, s.Cleanup); problem != nil {
				return result, done, problem
			}
			return s.describe(output{}), done, fail("authorization_canceled", "The device sign-in was cleared.")
		}
		if *expected != "" {
			return result, done, fail("authorization_canceled", "The pending sign-in was cleared.")
		}
		return result, done, fail("authorization_not_started", "Start a sign-in with ifytv auth login.")
	}
	if *expected != "" && *expected != p.Code {
		return result, done, fail("authorization_replaced", "A different sign-in has started. Check its verification code before continuing.")
	}
	*expected = p.Code
	if s.Cleanup != "" || !time.Now().Before(p.ExpiresAt) {
		code := "authorization_expired"
		if s.Cleanup == "logout" {
			code = "authorization_canceled"
		}
		result, problem = c.endPending(ctx, s, code)
		return result, done, problem
	}
	if time.Now().Before(p.NextPoll) {
		return result, p.nextWake(), nil
	}
	// Reserve this poll before the network request. A crash or a second waiter
	// cannot cause two exchanges inside the gateway's minimum interval.
	p.NextPoll = time.Now().Add(time.Duration(p.Interval) * time.Second)
	if problem := s.save(); problem != nil {
		return result, done, problem
	}
	payload, _ := json.Marshal(struct {
		Code string `json:"device_code"`
		Site string `json:"site"`
	}{p.Code, s.Site})
	var reply struct {
		Token     string `json:"access_token"`
		Scope     string `json:"run_scope"`
		ExpiresIn int64  `json:"expires_in"`
	}
	problem = c.devicePost(ctx, "token", payload, "", &reply)
	if problem != nil {
		switch problem.Code {
		case "authorization_denied", "authorization_expired":
			result, problem = c.endPending(ctx, s, problem.Code)
			return result, done, problem
		case "slow_down":
			p.Interval = min(p.Interval+5, maxDurationSeconds)
		case "authorization_pending":
		default:
			if !problem.retryable() || ctx.Err() != nil {
				return result, done, problem
			}
		}
		delay := max(time.Duration(p.Interval)*time.Second, problem.retryAfter)
		p.NextPoll = time.Now().Add(delay)
		if problem := s.save(); problem != nil {
			return result, done, problem
		}
		return result, p.nextWake(), nil
	}
	if !validDeviceToken(reply.Token) || !validRunScope(reply.Scope) || !validSeconds(reply.ExpiresIn) {
		return result, done, badDeviceReply()
	}
	c.rememberSecret(reply.Token)
	rejected := s.RunScope != "" && s.RunScope != reply.Scope
	s.Credential = &deviceCredential{Token: reply.Token, ExpiresAt: time.Now().Add(time.Duration(reply.ExpiresIn) * time.Second),
		LoginID: digest([]byte(p.Code))}
	s.Pending = nil
	if rejected {
		s.Cleanup = "scope_changed"
	} else {
		s.RunScope = reply.Scope
	}
	if problem := s.save(); problem != nil {
		return output{}, done, problem
	}
	if rejected {
		// Save the rejected credential before revoking. A subsequent login/logout
		// can finish cleanup if the gateway or this process disappears here.
		if problem := c.clearAuthorization(ctx, s, "scope_changed"); problem != nil {
			return s.describe(output{}), done, fail("authorization_scope_changed", "Another account approved this device. Run ifytv auth logout to finish revoking that approval before switching accounts.")
		}
		return s.describe(output{}), done, fail("authorization_scope_changed", "Another account approved this device. Sign in with the original account to resume, or use ifytv auth logout before intentionally switching accounts.")
	}
	return s.describe(output{}), done, nil
}

func (p *deviceChallenge) nextWake() time.Time {
	if p.ExpiresAt.Before(p.NextPoll) {
		return p.ExpiresAt
	}
	return p.NextPoll
}

func (c *client) endPending(ctx context.Context, s *deviceSession, code string) (output, *failure) {
	message := "The sign-in code expired. Run ifytv auth login to get a new code."
	if code == "authorization_denied" {
		s.Pending = nil // An explicit denial confirms no credential was issued.
		if problem := s.save(); problem != nil {
			return output{}, problem
		}
		message = "The browser declined this sign-in. Run ifytv auth login to start again."
	} else {
		if problem := c.clearAuthorization(ctx, s, "expired"); problem != nil {
			return s.describe(output{}), problem
		}
		if code == "authorization_canceled" {
			message = "The pending device sign-in was canceled."
		}
	}
	return s.describe(output{}), fail(code, message)
}
