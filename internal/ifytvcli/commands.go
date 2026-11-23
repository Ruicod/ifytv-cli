package ifytvcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

const help = `ifytv models [--json]
ifytv video create --spec FILE --run NAME [--json]
ifytv video status (--run NAME | --task UUID) [--json]
ifytv video wait (--run NAME | --task UUID) [--interval 4s] [--timeout DURATION] [--json]
ifytv video download (--run NAME | --task UUID) --out FILE [--json]
ifytv canvas import --project UUID (--run NAME | --task UUID) [--json]
ifytv --version

Set IFYTV_API_KEY for authenticated commands. IFYTV_BASE_URL selects the site.
Local references are relative to the JSON spec. Repeat create with the same
run name and inputs to resume. IFYTV_STATE_DIR overrides the private run directory.`

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var runNamePattern = regexp.MustCompile(`^[\pL\pN_-][\pL\pN\pM ._-]{0,127}$`)

func validUUID(id string) bool {
	return id != "00000000-0000-0000-0000-000000000000" && uuidPattern.MatchString(id)
}

type options struct {
	command, spec, run, task, project, out string
	interval, timeout                      time.Duration
}

func parseArgs(args []string) (options, *failure) {
	var o options
	if len(args) == 0 {
		o.command = "help"
		return o, nil
	}
	o.command = args[0]
	args = args[1:]
	if (o.command == "video" || o.command == "canvas") && len(args) > 0 {
		o.command += " " + args[0]
		args = args[1:]
	}
	f := flag.NewFlagSet("ifytv", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	switch o.command {
	case "--help", "-h", "help", "video --help", "canvas --help", "video -h", "canvas -h":
		o.command = "help"
	case "--version", "models":
	case "video create":
		f.StringVar(&o.spec, "spec", "", "")
		f.StringVar(&o.run, "run", "", "")
	case "video status", "video wait", "video download", "canvas import":
		f.StringVar(&o.run, "run", "", "")
		f.StringVar(&o.task, "task", "", "")
		switch o.command {
		case "video wait":
			f.DurationVar(&o.interval, "interval", 4*time.Second, "")
			f.DurationVar(&o.timeout, "timeout", 0, "")
		case "video download":
			f.StringVar(&o.out, "out", "", "")
		case "canvas import":
			f.StringVar(&o.project, "project", "", "")
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
	case "video create":
		if o.run == "" || o.spec == "" {
			return o, fail("usage", "Video create requires --spec FILE and --run NAME.")
		}
	case "video status", "video wait", "video download", "canvas import":
		if (o.run == "") == (o.task == "") {
			return o, fail("usage", "Supply exactly one of --run NAME or --task UUID.")
		}
		if o.task != "" && !validUUID(o.task) {
			return o, fail("invalid_task", "Supply a valid task UUID.")
		}
		if o.command == "canvas import" && !validUUID(o.project) {
			return o, fail("invalid_project", "Supply a project UUID with --project.")
		}
		if o.command == "video download" && o.out == "" {
			return o, fail("usage", "Video download requires --out FILE.")
		}
		if o.command == "video wait" && (o.interval <= 0 || o.timeout < 0) {
			return o, fail("usage", "Use a positive poll interval and a nonnegative timeout.")
		}
	}
	o.task = strings.ToLower(o.task)
	o.project = strings.ToLower(o.project)
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
	key := os.Getenv("IFYTV_API_KEY")
	var result output
	if problem == nil {
		result.Run, result.TaskID, result.ProjectID = o.run, o.task, o.project
		switch o.command {
		case "help":
			result.Help = help
		case "--version":
			result.Version = version
		default:
			var c *client
			c, problem = newClient(os.Getenv("IFYTV_BASE_URL"), key, o.command != "models")
			if problem == nil {
				c.progress = stderr
				result, problem = c.execute(ctx, o)
			}
		}
	}
	result.Error = problem
	if err := render(stdout, stderr, result, jsonMode, key); err != nil {
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
	}
	result.Run, result.TaskID, result.ProjectID = o.run, o.task, o.project
	if o.command == "video wait" && o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	if o.run != "" {
		task, problem := c.taskForRun(ctx, o.run)
		if problem != nil {
			return result, problem
		}
		result.TaskID = task
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
