package ifytvcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

const canvasContinuationVersion = 5

type savedRef struct {
	Hash string `json:"sha256"`
	URL  string `json:"url,omitempty"`
}

type record struct {
	Version            int             `json:"version"`
	ProjectID          string          `json:"project_id,omitempty"`
	ExpectedRevision   uint64          `json:"expected_revision,omitempty"`
	TargetNodeID       string          `json:"target_node_id,omitempty"`
	WorkspaceID        string          `json:"workspace_id,omitempty"`
	WorkspaceKind      string          `json:"workspace_kind,omitempty"`
	RequestedWorkspace string          `json:"requested_workspace_id,omitempty"`
	ProjectKey         string          `json:"project_key,omitempty"`
	Stage              string          `json:"stage"`
	SpecHash           string          `json:"spec_sha256"`
	Key                string          `json:"idempotency_key"`
	Refs               []savedRef      `json:"references"`
	Payload            json.RawMessage `json:"payload,omitempty"`
	Task               *taskReply      `json:"task,omitempty"`
}

func newKey() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

func stateRoot() (string, error) {
	if dir := os.Getenv("IFYTV_STATE_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	switch runtime.GOOS {
	case "windows", "darwin":
		var dir string
		var err error
		if runtime.GOOS == "windows" {
			dir, err = os.UserCacheDir()
		} else {
			dir, err = os.UserConfigDir()
		}
		return filepath.Join(dir, "ifytv", "state"), err
	default:
		dir := os.Getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(dir) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(dir, "ifytv"), nil
	}
}

type stateFile struct {
	path string
	lock *os.File
}

func (f *stateFile) unlock() {
	// The lock handle contains no buffered application data.
	_ = f.lock.Close()
}

var errUnsafeStateACL = errors.New("cannot establish private run state permissions")

func stateAccessFailure(err error, code, message string) *failure {
	if errors.Is(err, errUnsafeStateACL) {
		return fail("unsafe_state_permissions", "Cannot verify private local state. Restrict the state directory and files to your account, or set IFYTV_STATE_DIR to a private directory.")
	}
	return fail(code, message)
}

func (c *client) lockRun(ctx context.Context, name string) (*stateFile, *failure) {
	if !validHash(c.runScope) {
		return nil, fail("invalid_run_scope", "Cannot identify the account for this run. Sign in again.")
	}
	return lockState(ctx, "runs", c.runScope, digest([]byte(name)))
}

func lockState(ctx context.Context, category, scope, name string) (*stateFile, *failure) {
	root, err := stateRoot()
	if err != nil {
		return nil, fail("state_unavailable", "Cannot locate the private state directory. Set IFYTV_STATE_DIR.")
	}
	dir := filepath.Join(root, category, scope)
	prepareDir, openLock := ensureStateDir, openStateLock
	if category == "auth" {
		// A writable parent can replace a private child with another directory
		// or a symlink. Check the state root and protect the auth parent first.
		if err := ensureDeviceRoot(root); err != nil {
			return nil, stateAccessFailure(err, "state_unavailable", "Cannot access the state root for device sign-in.")
		}
		if err := ensureDeviceDir(filepath.Join(root, category)); err != nil {
			return nil, stateAccessFailure(err, "state_unavailable", "Cannot access the private device sign-in directory.")
		}
		prepareDir, openLock = ensureDeviceDir, openDeviceLock
	}
	if err := prepareDir(dir); err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot create the private state directory.")
	}
	stem := filepath.Join(dir, name)
	// Never unlink this lock file: replacing its inode could admit a second
	// writer. The kernel releases its lock when the process or handle exits.
	file, err := openLock(stem + ".lock")
	if err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot open the local state lock. Check the state directory permissions.")
	}
	for {
		if ctx.Err() != nil {
			file.Close()
			return nil, contextFailure(ctx)
		}
		acquired, err := fileops.TryLock(file)
		if err != nil {
			file.Close()
			return nil, fail("state_lock_failed", "Cannot lock the local state file.")
		}
		if acquired {
			return &stateFile{stem + ".json", file}, nil
		}
		if problem := pause(ctx, 50*time.Millisecond); problem != nil {
			file.Close()
			return nil, problem
		}
	}
}

func corruptState() *failure {
	return fail("invalid_run_state", "The saved run is incomplete or damaged. Restore its state before retrying.")
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (r *record) valid() bool {
	if r.Version != 4 && r.WorkspaceKind != "" {
		return false
	}
	if r.Version != canvasContinuationVersion && (r.ExpectedRevision != 0 || r.TargetNodeID != "") {
		return false
	}
	switch r.Version {
	case 1, 2:
		if r.ProjectKey != "" || r.WorkspaceID != "" || r.RequestedWorkspace != "" || (r.Version == 1 && r.ProjectID != "") || (r.Version == 2 && !validUUID(r.ProjectID)) {
			return false
		}
	case 3, 4:
		if !validUUID(r.ProjectKey) || r.ProjectKey == r.Key ||
			(r.RequestedWorkspace != "" && (!validUUID(r.RequestedWorkspace) || r.WorkspaceID != "")) ||
			(r.WorkspaceID != "" && !validUUID(r.WorkspaceID)) ||
			(r.ProjectID != "" && (r.WorkspaceID == "" || !validUUID(r.ProjectID))) {
			return false
		}
		if r.Version == 4 {
			switch r.WorkspaceKind {
			case personalWorkspaceKind:
				// The personal route returns both IDs together.
				if r.RequestedWorkspace != "" || (r.WorkspaceID == "") != (r.ProjectID == "") {
					return false
				}
			case teamWorkspaceKind:
				if r.WorkspaceID == "" && r.RequestedWorkspace == "" {
					return false
				}
			default:
				return false
			}
		}
		if r.ProjectID == "" && (r.Stage == "prepared" || r.Stage == "submitted") {
			return false
		}
	case canvasContinuationVersion:
		if !validUUID(r.ProjectID) || r.ExpectedRevision == 0 || (r.TargetNodeID != "" && !nodePattern.MatchString(r.TargetNodeID)) ||
			r.ProjectKey != "" || r.WorkspaceID != "" || r.RequestedWorkspace != "" {
			return false
		}
	default:
		return false
	}
	if r.Stage == "video" || (r.ProjectKey != "" && r.Stage == "canvas") {
		keyMatches := r.Key == ""
		if r.Version == canvasContinuationVersion {
			keyMatches = validUUID(r.Key)
		}
		return keyMatches && r.SpecHash == "" && len(r.Refs) == 0 && len(r.Payload) == 0 && r.Task == nil
	}
	if !validUUID(r.Key) || !validHash(r.SpecHash) {
		return false
	}
	for _, ref := range r.Refs {
		if !validHash(ref.Hash) || (ref.URL != "" && !validMediaURL(ref.URL)) || (r.Stage != "uploading" && ref.URL == "") {
			return false
		}
	}
	switch r.Stage {
	case "uploading":
		return len(r.Payload) == 0 && r.Task == nil
	case "prepared", "submitted":
		payload := r.Payload
		if r.ProjectID != "" {
			var body canvasRunRequest
			if json.Unmarshal(payload, &body) != nil || len(body.Names) != len(r.Refs) ||
				body.ExpectedRevision != r.ExpectedRevision || body.TargetNodeID != r.TargetNodeID {
				return false
			}
			payload = body.Spec
		}
		var spec videoSpec
		if json.Unmarshal(payload, &spec) != nil || spec.Model == "" {
			return false
		}
		if r.Version == canvasContinuationVersion {
			refs := spec.references()
			if len(refs) != len(r.Refs) {
				return false
			}
			for i, ref := range refs {
				if *ref.value != r.Refs[i].URL {
					return false
				}
			}
		}
		if r.Stage == "prepared" {
			return r.Task == nil
		}
		return r.Task != nil && r.validateTask(r.Task) == nil
	}
	return false
}

func (f *stateFile) load() (*record, *failure) {
	file, err := openStateRecord(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot open the saved run.")
	}
	defer file.Close()
	const maxState = 32 << 20
	b, err := io.ReadAll(io.LimitReader(file, maxState+1))
	var r record
	if err != nil || len(b) > maxState || json.Unmarshal(b, &r) != nil || !r.valid() {
		return nil, corruptState()
	}
	return &r, nil
}

func (f *stateFile) save(r *record) *failure {
	return f.writeJSON(r, "Could not save the run. Check the state directory and retry the same run.")
}

func (f *stateFile) writeJSON(value any, message string) *failure {
	temp, err := createStateTemp(filepath.Dir(f.path))
	if err != nil {
		return stateAccessFailure(err, "state_write_failed", message)
	}
	defer os.Remove(temp.Name())
	err = json.NewEncoder(temp).Encode(value)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return fail("state_write_failed", message)
	}
	if err := fileops.Publish(temp.Name(), f.path, true); err != nil {
		return fail("state_write_failed", message)
	}
	return nil
}

func (c *client) submittedRun(ctx context.Context, name string) (*record, *failure) {
	file, problem := c.lockRun(ctx, name)
	if problem != nil {
		return nil, problem
	}
	defer file.unlock()
	r, problem := file.load()
	if problem != nil {
		return nil, problem
	}
	if r == nil {
		return nil, fail("run_not_found", "No run with this name exists for the current site and key.")
	}
	if r.Task == nil {
		if r.Stage == "canvas" {
			return r, fail("run_pending", "This run is for a canvas only. Repeat canvas create with the same run name to recover its link.")
		}
		return r, fail("run_pending", "The run has no confirmed task ID yet. Repeat video create with the same spec and run.")
	}
	return r, nil
}
