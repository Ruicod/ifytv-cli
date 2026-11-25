package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/Ruicod/ifytv-cli/internal/install"
)

type failure struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	retryAfter    time.Duration
	invalidDevice bool
}

func fail(code, message string) *failure { return &failure{Code: code, Message: message} }

func contextFailure(ctx context.Context) *failure {
	if ctx.Err() == context.DeadlineExceeded {
		return fail("timeout", "The operation timed out. Resume using the same run or task.")
	}
	return fail("canceled", "Operation interrupted. Resume using the same run or task.")
}

func (f *failure) retryable() bool {
	if f.Code == "canvas_asset_daily_limit" {
		return false
	}
	return f.Code == "network_error" || f.HTTPStatus == 429 || f.HTTPStatus == 502 || f.HTTPStatus == 503 || f.HTTPStatus == 504
}

type Task struct {
	Status    string `json:"status,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Progress  *int   `json:"progress,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type output struct {
	Task
	TaskID             string             `json:"task_id,omitempty"`
	Run                string             `json:"run,omitempty"`
	WorkspaceID        string             `json:"workspace_id,omitempty"`
	WorkspaceKind      string             `json:"workspace_kind,omitempty"`
	Workspaces         *[]canvasWorkspace `json:"workspaces,omitempty"`
	DefaultWorkspaceID string             `json:"default_workspace_id,omitempty"`
	ProjectID          string             `json:"project_id,omitempty"`
	NodeID             string             `json:"node_id,omitempty"`
	AssetID            string             `json:"asset_id,omitempty"`
	ProjectURL         string             `json:"project_url,omitempty"`
	Duplicate          *bool              `json:"duplicate,omitempty"`
	AuthorizationURL   string             `json:"authorization_url,omitempty"`
	AuthorizationID    string             `json:"authorization_id,omitempty"`
	UserCode           string             `json:"user_code,omitempty"`
	Source             string             `json:"source,omitempty"`
	Path               string             `json:"path,omitempty"`
	Bytes              int64              `json:"bytes,omitempty"`
	Models             *[]capability      `json:"models,omitempty"`
	Version            string             `json:"version,omitempty"`
	Help               string             `json:"help,omitempty"`
	Installation       *install.Result    `json:"installation,omitempty"`
	Error              *failure           `json:"error,omitempty"`
}

func render(stdout, stderr io.Writer, result output, jsonMode bool, secrets ...string) error {
	if jsonMode {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		// Scrub string values before serialization so even punctuation in a key
		// cannot corrupt the JSON or leak through JSON escaping.
		for _, key := range secrets {
			quoted, _ := json.Marshal(key)
			if key != "" && bytes.Contains(data, quoted[1:len(quoted)-1]) {
				var value map[string]any
				if err := json.Unmarshal(data, &value); err != nil {
					return err
				}
				for _, secret := range secrets {
					if secret != "" {
						redactValues(value, secret)
					}
				}
				return json.NewEncoder(stdout).Encode(value)
			}
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	}
	var lines []string
	dest := stdout
	if result.Error != nil {
		dest = stderr
		lines = append(lines, result.Error.Code+": "+result.Error.Message)
	}
	if result.Help != "" {
		lines = append(lines, strings.Split(result.Help, "\n")...)
	}
	if result.Version != "" {
		lines = append(lines, result.Version)
	}
	if installed := result.Installation; installed != nil {
		lines = append(lines, "Installed the IFYTV CLI at "+installed.Executable)
		for _, dir := range installed.Skills {
			lines = append(lines, "Installed the IFYTV Skill at "+dir)
		}
		for _, file := range installed.Backups {
			lines = append(lines, "Saved the previous file at "+file)
		}
		if installed.Portable {
			lines = append(lines, "To use ifytv by name, add "+filepath.Dir(installed.Executable)+" to your terminal's PATH.")
		}
		lines = append(lines, "Open a new terminal and run ifytv --version. Restart your Agent and start a new chat to use the Skill.")
		if runtime.GOOS == "windows" && !installed.Portable {
			lines = append(lines, "If a system-wide copy takes precedence, Get-Command ifytv -All shows the order. You can run the executable above directly.")
		}
	}
	if result.Models != nil {
		for _, m := range *result.Models {
			lines = append(lines, m.ID+"  "+m.DisplayName)
		}
	}
	if result.Workspaces != nil {
		for _, workspace := range *result.Workspaces {
			label := workspace.ID + "  " + workspace.Name
			if workspace.ID == result.DefaultWorkspaceID {
				label += " (default)"
			}
			lines = append(lines, label)
		}
	}
	if result.TaskID != "" {
		lines = append(lines, strings.TrimSpace(result.TaskID+" "+result.Status))
	}
	if result.Path != "" {
		lines = append(lines, result.Path)
	}
	if result.ProjectURL != "" {
		lines = append(lines, result.ProjectURL)
	}
	if result.AuthorizationURL != "" {
		if result.UserCode != "" {
			lines = append(lines, "Open "+result.AuthorizationURL+" and confirm code "+result.UserCode+".",
				"Run ifytv auth wait --authorization-id "+result.AuthorizationID+" to finish this sign-in.")
		} else {
			lines = append(lines, "Authorize this project at "+result.AuthorizationURL)
		}
	} else if result.Status == "authenticated" {
		if result.Source == "environment" {
			lines = append(lines, "Authenticated using IFYTV_API_KEY.")
		} else {
			lines = append(lines, "Authenticated on this device.")
		}
	} else if result.Status == "signed_out" {
		lines = append(lines, "Signed out.")
	} else if result.Status == "cancellation_pending" {
		lines = append(lines, "Device sign-in cancellation is pending. Run ifytv auth logout to finish.")
	}
	for _, line := range lines {
		for _, key := range secrets {
			if key != "" {
				line = strings.ReplaceAll(line, key, "[redacted]")
			}
		}
		line = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, line)
		if _, err := fmt.Fprintln(dest, line); err != nil {
			return err
		}
	}
	return nil
}

func redactValues(v any, key string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, key, "[redacted]")
	case map[string]any:
		for k, v := range x {
			x[k] = redactValues(v, key)
		}
	case []any:
		for i, v := range x {
			x[i] = redactValues(v, key)
		}
	}
	return v
}
