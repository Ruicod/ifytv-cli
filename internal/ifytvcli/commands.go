package ifytvcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Ruicod/ifytv-cli/internal/install"
)

const help = `ifytv install [--agent auto|codex|claude-code|cursor|gemini-cli|github-copilot|opencode|windsurf|kiro-cli|openclaw|both] [--json]
ifytv models [--json]
ifytv auth login [--json]
ifytv auth wait [--authorization-id ID] [--timeout DURATION] [--json]
ifytv auth status [--json]
ifytv auth logout [--json]
ifytv canvas workspaces [--json]
ifytv canvas create --run NAME [--workspace personal|TEAM_UUID] [--json]
ifytv video create --spec FILE --run NAME [--project UUID|URL|auto] [--workspace personal|TEAM_UUID] [--no-canvas] [--json]
ifytv video status (--run NAME | --task UUID) [--json]
ifytv video wait (--run NAME | --task UUID) [--interval 4s] [--timeout DURATION] [--json]
ifytv video download (--run NAME | --task UUID) --out FILE [--json]
ifytv canvas import --project UUID|URL (--run NAME | --task UUID) [--json]
ifytv --version

Install copies this executable and its Agent Skill for the current user.
The default installs the Skill for Codex, Claude Code and other detected Agents.
IFYTV_INSTALL_HOME selects a portable installation without changing PATH.
Sign in with ifytv auth login, open the printed link, then run ifytv auth wait.
IFYTV_API_KEY overrides the saved login. IFYTV_BASE_URL selects the site.
Use --authorization-id ID on video and canvas commands to require one specific device approval.
New runs use My Canvases in the signed-in account's personal cloud by default.
Use --project URL or UUID to reuse a cloud canvas. Use --workspace TEAM_UUID
to create in a particular team, or --no-canvas for a video task without a canvas.
The canvas shows the references and prompt before generation.
The same video card shows progress and plays the result. Repeat create with the same
run name and inputs to resume. IFYTV_STATE_DIR overrides the private run directory.`

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var runNamePattern = regexp.MustCompile(`^[\pL\pN_-][\pL\pN\pM ._-]{0,127}$`)

func validUUID(id string) bool {
	return id != "00000000-0000-0000-0000-000000000000" && uuidPattern.MatchString(id)
}

type options struct {
	command, spec, run, task, project, out string
	authorizationID, agent, workspace      string
	autoProject, noCanvas                  bool
	interval, timeout                      time.Duration
}

func projectTarget(value string) (string, *failure) {
	if validUUID(value) {
		return strings.ToLower(value), nil
	}
	invalid := fail("invalid_project", "Supply a project UUID or a cloud canvas URL from this site.")
	u, err := url.Parse(value)
	if err != nil || !webURL(u) || u.RawPath != "" {
		return "", invalid
	}
	c, problem := newClient(os.Getenv("IFYTV_BASE_URL"), "", false)
	if problem != nil {
		return "", problem
	}
	if origin(u) != origin(c.base) {
		return "", fail("project_site_mismatch", "The canvas link belongs to another site. Select the matching IFYTV_BASE_URL and account.")
	}
	id, ok := strings.CutPrefix(strings.TrimSuffix(u.Path, "/"), c.base.Path+"/canvas/cloud/")
	if !ok || !validUUID(id) {
		return "", invalid
	}
	return strings.ToLower(id), nil
}

func parseArgs(args []string) (options, *failure) {
	var o options
	if len(args) == 0 {
		o.command = "help"
		return o, nil
	}
	o.command = args[0]
	args = args[1:]
	if (o.command == "video" || o.command == "canvas" || o.command == "auth") && len(args) > 0 {
		o.command += " " + args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("ifytv", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	projectRequested := false
	if o.command == "video create" || o.command == "canvas import" {
		f.Func("project", "", func(value string) error {
			if projectRequested && value != o.project {
				return errors.New("conflicting projects")
			}
			o.project, projectRequested = value, true
			return nil
		})
	}
	switch o.command {
	case "auth wait", "video create", "video status", "video wait", "video download", "canvas import", "canvas create", "canvas workspaces":
		f.Func("authorization-id", "", func(id string) error {
			if !validRunScope(id) || (o.authorizationID != "" && o.authorizationID != id) {
				return errors.New("invalid authorization ID")
			}
			o.authorizationID = id
			return nil
		})
	}
	if o.command == "video create" || o.command == "canvas create" {
		f.Func("workspace", "", func(id string) error {
			if (id != personalWorkspaceKind && !validUUID(id)) || (o.workspace != "" && !strings.EqualFold(id, o.workspace)) {
				return errors.New("invalid workspace")
			}
			o.workspace = strings.ToLower(id)
			return nil
		})
	}
	switch o.command {
	case "--help", "-h", "help", "video --help", "canvas --help", "auth --help", "video -h", "canvas -h", "auth -h":
		o.command = "help"
	case "--version", "models", "auth login", "auth status", "auth logout", "canvas workspaces":
	case "install":
		f.StringVar(&o.agent, "agent", "auto", "")
	case "auth wait":
		f.DurationVar(&o.timeout, "timeout", 0, "")
	case "canvas create":
		o.autoProject = true
		f.StringVar(&o.run, "run", "", "")
	case "video create":
		f.StringVar(&o.spec, "spec", "", "")
		f.StringVar(&o.run, "run", "", "")
		f.BoolVar(&o.noCanvas, "no-canvas", false, "")
	case "video status", "video wait", "video download", "canvas import":
		f.StringVar(&o.run, "run", "", "")
		f.StringVar(&o.task, "task", "", "")
		switch o.command {
		case "video wait":
			f.DurationVar(&o.interval, "interval", 4*time.Second, "")
			f.DurationVar(&o.timeout, "timeout", 0, "")
		case "video download":
			f.StringVar(&o.out, "out", "", "")
		}
	default:
		return o, fail("usage", "Unknown command. See ifytv --help.")
	}
	if err := f.Parse(args); errors.Is(err, flag.ErrHelp) {
		o.command = "help"
		return o, nil
	} else if err != nil || f.NArg() != 0 {
		return o, fail("usage", "Invalid command arguments. See ifytv --help.")
	}
	if o.run != "" && (!runNamePattern.MatchString(o.run) || strings.TrimSpace(o.run) != o.run) {
		return o, fail("invalid_run", "Use a run name of up to 128 letters, numbers, spaces, dots, dashes or underscores.")
	}
	switch o.command {
	case "canvas create":
		if o.run == "" {
			return o, fail("usage", "Canvas create requires --run NAME.")
		}
	case "auth wait":
		if o.timeout < 0 {
			return o, fail("usage", "Use a nonnegative timeout.")
		}
	case "video create":
		if o.run == "" || o.spec == "" {
			return o, fail("usage", "Video create requires --spec FILE and --run NAME.")
		}
		if projectRequested {
			if o.project == "auto" {
				o.autoProject, o.project = true, ""
			} else {
				var problem *failure
				if o.project, problem = projectTarget(o.project); problem != nil {
					return o, problem
				}
			}
		}
		if o.noCanvas && (projectRequested || o.workspace != "") {
			return o, fail("usage", "Use --no-canvas on its own to submit a video without a canvas.")
		}
		if o.project != "" {
			// The workspace flag selects where to create a new canvas. A supplied
			// project is already the destination, regardless of its workspace.
			o.workspace = ""
		}
	case "video status", "video wait", "video download", "canvas import":
		if (o.run == "") == (o.task == "") {
			return o, fail("usage", "Supply exactly one of --run NAME or --task UUID.")
		}
		if o.task != "" && !validUUID(o.task) {
			return o, fail("invalid_task", "Supply a valid task UUID.")
		}
		if o.command == "canvas import" {
			var problem *failure
			if o.project, problem = projectTarget(o.project); problem != nil {
				return o, problem
			}
		}
		if o.command == "video download" && o.out == "" {
			return o, fail("usage", "Video download requires --out FILE.")
		}
		if o.command == "video wait" && (o.interval <= 0 || o.timeout < 0) {
			return o, fail("usage", "Use a positive poll interval and a nonnegative timeout.")
		}
	}
	o.task = strings.ToLower(o.task)
	return o, nil
}

// Execute runs one command and returns its process exit status.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	var jsonMode bool
	var rest []string
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
		} else {
			rest = append(rest, arg)
		}
	}
	o, problem := parseArgs(rest)
	var secrets []string
	var result output
	if problem == nil {
		result.Run, result.TaskID, result.ProjectID = o.run, o.task, o.project
		switch o.command {
		case "help":
			result.Help = help
		case "--version":
			result.Version = version
		case "install":
			result.Installation, problem = installCommand(o.agent)
		default:
			key := os.Getenv("IFYTV_API_KEY")
			secrets = append(secrets, key)
			var c *client
			c, problem = newClient(os.Getenv("IFYTV_BASE_URL"), key, o.command != "models")
			if problem == nil {
				c.progress = stderr
				result, problem = c.runCommand(ctx, o, result)
				secrets = append(secrets, c.secrets...)
			}
		}
	}
	result.Error = problem
	if err := render(stdout, stderr, result, jsonMode, secrets...); err != nil {
		return 1
	}
	if problem != nil {
		if problem.Code == "usage" {
			return 2
		}
		return 1
	}
	return 0
}

func installCommand(agent string) (*install.Result, *failure) {
	home, portable, err := install.UserHome()
	if err != nil {
		return nil, fail("install_failed", err.Error())
	}
	dirs, err := installSkillRoots(home, agent)
	if err != nil {
		return nil, fail("usage", err.Error())
	}
	installed, err := install.Run(home, dirs, portable)
	if err != nil {
		return nil, fail("install_failed", err.Error())
	}
	return &installed, nil
}

func (c *client) execute(ctx context.Context, o options) (output, *failure) {
	var result output
	switch o.command {
	case "models":
		models, problem := c.models(ctx)
		if problem == nil {
			result.Models = &models
		}
		return result, problem
	case "video create":
		return c.create(ctx, o)
	case "canvas create":
		return c.createCanvasForRun(ctx, o)
	case "canvas workspaces":
		workspaces, preferred, problem := c.canvasWorkspaces(ctx)
		if problem == nil {
			result.Workspaces, result.DefaultWorkspaceID = &workspaces, preferred
		}
		return result, problem
	}
	result.Run, result.TaskID, result.ProjectID = o.run, o.task, o.project
	if o.run != "" {
		r, problem := c.submittedRun(ctx, o.run)
		if r != nil && o.command != "canvas import" {
			result.WorkspaceID, result.WorkspaceKind = r.WorkspaceID, r.WorkspaceKind
			c.addCanvasLocation(&result, r.ProjectID, "")
		}
		if problem != nil {
			return result, problem
		}
		result.TaskID = r.Task.ID
		if o.command != "canvas import" {
			c.addCanvasLocation(&result, r.ProjectID, r.Task.NodeID)
		}
	}
	switch o.command {
	case "canvas import":
		return c.importVideo(ctx, result)
	case "video download":
		return c.download(ctx, result, o.out)
	}
	lastProgress := ""
	for {
		info, problem := c.status(ctx, result.TaskID)
		modelFinished := problem == nil && info.Status == "succeeded"
		historyExpired := problem != nil && problem.HTTPStatus == http.StatusNotFound && problem.Code == "task_not_found"
		if o.command == "video wait" && result.NodeID != "" && (modelFinished || historyExpired) {
			// Persisted card receipts can outlive task history. Import checks
			// the target and its receipt before looking up a task or media.
			if modelFinished {
				result.Task = info
			}
			result, problem = c.importVideo(ctx, result)
			if problem == nil {
				result.Status = "succeeded"
				return result, nil
			}
		}
		if problem == nil {
			result.Task = info
			if info.Status == "failed" {
				return withTaskStatus(result)
			}
			if o.command != "video wait" || info.Status == "succeeded" {
				return result, nil
			}
			current := info.Status
			if info.Progress != nil {
				current += fmt.Sprintf(" %d%%", *info.Progress)
			}
			if current != lastProgress {
				c.report(current)
				lastProgress = current
			}
		} else if o.command != "video wait" || !problem.retryable() {
			return result, problem
		} else if lastProgress != problem.Message {
			c.report(problem.Message)
			lastProgress = problem.Message
		}
		delay := o.interval
		if problem != nil && problem.retryAfter > delay {
			delay = problem.retryAfter
		}
		if problem := pause(ctx, delay); problem != nil {
			return result, problem
		}
	}
}

func pause(ctx context.Context, duration time.Duration) *failure {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return contextFailure(ctx)
	case <-timer.C:
		return nil
	}
}
