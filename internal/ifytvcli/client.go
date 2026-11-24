package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const defaultSite = "https://video.fastaitoken.com"
const maxJSONBytes = 4 << 20

type client struct {
	base     *url.URL
	key      string
	runScope string
	secrets  []string
	http     *http.Client
	progress io.Writer
}

func (c *client) report(message string) {
	if c.progress == nil {
		return
	}
	if c.key != "" {
		message = strings.ReplaceAll(message, c.key, "[redacted]")
	}
	// Diagnostics are best effort; the command result has its own output stream.
	fmt.Fprintln(c.progress, message)
}

func newClient(site, key string, auth bool) (*client, *failure) {
	if site == "" {
		site = defaultSite
	}
	u, err := url.Parse(site)
	if err != nil || !webURL(u) || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, fail("invalid_base_url", "Set IFYTV_BASE_URL to an HTTP(S) site without credentials, query parameters or a fragment.")
	}
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if auth {
		if u.Scheme == "http" && u.Hostname() != "localhost" && !net.ParseIP(u.Hostname()).IsLoopback() {
			return nil, fail("insecure_base_url", "Authenticated commands require HTTPS outside localhost and loopback IP addresses.")
		}
		if len(key) > 4096 || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, fail("invalid_api_key", "Check IFYTV_API_KEY for whitespace or invalid characters.")
		}
	} else {
		key = ""
	}
	c := &client{base: u, key: key, http: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: checkRedirect}}
	if key != "" && !strings.HasPrefix(key, deviceTokenPrefix) {
		c.runScope = digest([]byte(u.String() + "\x00" + key))
	}
	return c, nil
}

func webURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == ""
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

var errRedirect = errors.New("unsafe or excessive redirect")

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 || !webURL(req.URL) || via[0].Method != http.MethodGet || req.Method != http.MethodGet ||
		(via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https") {
		return errRedirect
	}
	// net/http can copy sensitive headers to subdomains and across ports.
	// Never restore them later in a chain that has left the original origin.
	trusted := origin(via[0].URL)
	req.Header.Del("Referer")
	for _, hop := range append(via, req) {
		if origin(hop.URL) != trusted {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			req.Header.Del("Idempotency-Key")
			break
		}
	}
	return nil
}

func (c *client) request(ctx context.Context, method, path string, body io.Reader, auth bool) (*http.Request, *failure) {
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, body)
	if err != nil {
		return nil, fail("invalid_request", "Could not build the request. Check IFYTV_BASE_URL.")
	}
	if auth {
		if c.key == "" {
			return nil, fail("authorization_required", "Sign in with ifytv auth login.")
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	return req, nil
}

func (c *client) send(req *http.Request) (*http.Response, *failure) {
	resp, err := c.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, contextFailure(req.Context())
		}
		if errors.Is(err, errRedirect) {
			return nil, fail("redirect_rejected", "The site returned an unsafe or excessive redirect.")
		}
		return nil, fail("network_error", "The site did not return a complete response. Retry the same command.")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
		code := safeCode(body.Error.Code)
		if code == "" {
			code = "http_error"
		}
		problem := &failure{Code: code, Message: httpMessage(resp.StatusCode), HTTPStatus: resp.StatusCode, retryAfter: retryDelay(resp.Header.Get("Retry-After"))}
		// A download may finish on a CDN. Only the credentialed site itself
		// can declare this machine's saved device session invalid.
		problem.invalidDevice = resp.StatusCode == 401 && code == "device_unauthorized" && c.key != "" &&
			origin(resp.Request.URL) == origin(c.base) && resp.Request.Header.Get("Authorization") == "Bearer "+c.key
		return nil, problem
	}
	return resp, nil
}

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

func safeCode(s string) string {
	if codePattern.MatchString(s) {
		return s
	}
	return ""
}

func httpMessage(status int) string {
	switch status {
	case 400:
		return "The site rejected the request parameters."
	case 401:
		return "Authentication failed. Check IFYTV_API_KEY."
	case 402:
		return "The site reported insufficient credit."
	case 403:
		return "The key does not have permission for this operation."
	case 404:
		return "The requested resource was not found for this key."
	case 409:
		return "The site reported a conflict. Check the task or run before retrying."
	case 410:
		return "The requested resource has expired."
	case 413:
		return "The uploaded file exceeds the site's limit."
	case 429:
		return "The site is rate limiting requests. Retry the same command later."
	case 507:
		return "The site has insufficient storage."
	}
	return "The site returned HTTP " + strconv.Itoa(status) + "."
}

func retryDelay(value string) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(when))
	}
	return 0
}

func readJSON(r io.Reader, target any) *failure {
	b, err := io.ReadAll(io.LimitReader(r, maxJSONBytes+1))
	if err != nil || len(b) > maxJSONBytes || json.Unmarshal(b, target) != nil {
		return fail("invalid_response", "The site returned incomplete or invalid JSON. Retry the same command.")
	}
	return nil
}

func (c *client) json(ctx context.Context, method, path string, payload []byte, key string, auth bool, target any) *failure {
	req, problem := c.request(ctx, method, path, bytes.NewReader(payload), auth)
	if problem != nil {
		return problem
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
		// The Go transport treats this header as permission to replay a POST
		// on a reused connection. Leave a lost reply for explicit run recovery.
		req.GetBody = nil
	}
	resp, problem := c.send(req)
	if problem != nil {
		return problem
	}
	defer resp.Body.Close()
	return readJSON(resp.Body, target)
}

func (c *client) models(ctx context.Context) ([]capability, *failure) {
	var catalog struct {
		Models []capability `json:"models"`
	}
	if problem := c.json(ctx, "GET", "/api/v1/models", nil, "", false, &catalog); problem != nil {
		return nil, problem
	}
	if catalog.Models == nil {
		return nil, fail("invalid_catalog", "The site returned an invalid model catalog.")
	}
	return catalog.Models, nil
}

type taskReply struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	Task
}

func (t *taskReply) validateProject(project string) *failure {
	if project == "" {
		t.ProjectID, t.NodeID = "", ""
		return nil
	}
	if t.ProjectID != project || !nodePattern.MatchString(t.NodeID) {
		return fail("invalid_response", "The site did not identify the video card in the requested project. Retry the same run.")
	}
	return nil
}

func (c *client) addCanvasLocation(result *output, project, node string) {
	if project != "" {
		result.ProjectID, result.NodeID = project, node
		result.ProjectURL = c.base.String() + "/canvas/cloud/" + project
		if node != "" {
			result.ProjectURL += "?node=" + url.QueryEscape(node)
		}
	}
}

func (c *client) addProjectConsent(result *output, problem *failure) {
	if problem.HTTPStatus == http.StatusForbidden &&
		(problem.Code == "canvas_forbidden" || problem.Code == "cli_grant_required" || problem.Code == "forbidden") {
		result.AuthorizationURL = c.base.String() + "/cli?project=" + result.ProjectID
	}
}

func (t *taskReply) validate(expected string) *failure {
	t.ID = strings.ToLower(t.ID)
	if !validUUID(t.ID) || (expected != "" && t.ID != expected) {
		return fail("invalid_response", "The site returned a missing or unexpected task ID.")
	}
	switch t.Status {
	case "queued", "submitting", "processing", "succeeded", "failed":
	default:
		return fail("invalid_response", "The site returned an unknown task status.")
	}
	t.Phase = safeCode(t.Phase)
	t.ErrorCode = safeCode(t.ErrorCode)
	if t.Progress != nil && (*t.Progress < 0 || *t.Progress > 100) {
		t.Progress = nil
	}
	return nil
}

func (c *client) status(ctx context.Context, id string) (Task, *failure) {
	var reply taskReply
	if problem := c.json(ctx, "GET", "/api/v1/tasks/"+id, nil, "", true, &reply); problem != nil {
		return Task{}, problem
	}
	problem := reply.validate(id)
	return reply.Task, problem
}

var nodePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)

func (c *client) importVideo(ctx context.Context, result output) (output, *failure) {
	payload, _ := json.Marshal(struct {
		Task string `json:"task_id"`
	}{result.TaskID})
	var reply struct {
		Project   string `json:"project_id"`
		Node      string `json:"node_id"`
		Asset     string `json:"asset_id"`
		Link      string `json:"project_url"`
		Duplicate *bool  `json:"duplicate"`
	}
	problem := c.json(ctx, "POST", "/api/v1/cli/projects/"+result.ProjectID+"/videos", payload, "", true, &reply)
	if problem != nil {
		c.addProjectConsent(&result, problem)
		if problem.Code == "canvas_asset_daily_limit" {
			problem.Message = "The project's daily upload allowance is used up. When it resets, save this task to the same canvas card."
		}
		return result, problem
	}
	if !strings.EqualFold(reply.Project, result.ProjectID) || !nodePattern.MatchString(reply.Node) || !validUUID(reply.Asset) || reply.Duplicate == nil ||
		(result.NodeID != "" && result.NodeID != reply.Node) {
		return result, fail("invalid_response", "The site returned an incomplete import receipt. Retry the import for this task.")
	}
	requestedNode := result.NodeID
	result.NodeID, result.AssetID, result.Duplicate = reply.Node, reply.Asset, reply.Duplicate
	// The public cloud-project route is stable. Only surface a node query when
	// it identifies this receipt's node; never emit a server-supplied bearer URL.
	result.ProjectURL = c.base.String() + "/canvas/cloud/" + result.ProjectID
	if u, err := url.Parse(reply.Link); requestedNode == reply.Node || (err == nil && u.Query().Get("node") == reply.Node) {
		result.ProjectURL += "?node=" + url.QueryEscape(reply.Node)
	}
	return result, nil
}
