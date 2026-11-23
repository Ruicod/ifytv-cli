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
)

type savedRef struct {
	Hash string `json:"sha256"`
	URL  string `json:"url,omitempty"`
}

type record struct {
	Version  int             `json:"version"`
	Stage    string          `json:"stage"`
	SpecHash string          `json:"spec_sha256"`
	Key      string          `json:"idempotency_key"`
	Refs     []savedRef      `json:"references"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Task     *taskReply      `json:"task,omitempty"`
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

type runFile struct {
	path string
	lock *os.File
}

var errUnsafeStateACL = errors.New("cannot establish private run state permissions")

func stateAccessFailure(err error, code, message string) *failure {
	if errors.Is(err, errUnsafeStateACL) {
		return fail("unsafe_state_permissions", "Cannot verify private Windows run state. Restrict the existing run to your Windows account and administrators, or set IFYTV_STATE_DIR to a private folder on an ACL-capable drive.")
	}
	return fail(code, message)
}

func (c *client) lockRun(ctx context.Context, name string) (*runFile, *failure) {
	root, err := stateRoot()
	if err != nil {
		return nil, fail("state_unavailable", "Cannot locate the private state directory. Set IFYTV_STATE_DIR.")
	}
	scope := digest([]byte(c.base.String() + "\x00" + c.key))
	dir := filepath.Join(root, "runs", scope)
	if err := ensureStateDir(dir); err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot create the private state directory.")
	}
	stem := filepath.Join(dir, digest([]byte(name)))
	// Never unlink this lock file: replacing its inode could admit a second
	// writer. The kernel releases its lock when the process or handle exits.
	file, err := openStateLock(stem + ".lock")
	if err != nil {
		return nil, stateAccessFailure(err, "state_unavailable", "Cannot open the run lock. Check the state directory permissions.")
	}
	for {
		if ctx.Err() != nil {
			file.Close()
			return nil, contextFailure(ctx)
		}
		acquired, err := tryFileLock(file)
		if err != nil {
			file.Close()
			return nil, fail("state_lock_failed", "Cannot lock this run in the state directory.")
		}
		if acquired {
			return &runFile{stem + ".json", file}, nil
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
	if r.Version != 1 || !validUUID(r.Key) || !validHash(r.SpecHash) {
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
		var spec videoSpec
		if len(r.Payload) == 0 || json.Unmarshal(r.Payload, &spec) != nil || spec.Model == "" {
			return false
		}
		if r.Stage == "prepared" {
			return r.Task == nil
		}
		return r.Task != nil && r.Task.validate("") == nil
	}
	return false
}

func (f *runFile) load() (*record, *failure) {
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

func (f *runFile) save(r *record) *failure {
	const message = "Could not save the run. Check the state directory and retry the same run."
	temp, err := createStateTemp(filepath.Dir(f.path))
	if err != nil {
		return stateAccessFailure(err, "state_write_failed", message)
	}
	defer os.Remove(temp.Name())
	err = json.NewEncoder(temp).Encode(r)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil || closeErr != nil {
		return fail("state_write_failed", message)
	}
	if err := publishFile(temp.Name(), f.path, true); err != nil {
		return fail("state_write_failed", message)
	}
	return nil
}

func (c *client) taskForRun(ctx context.Context, name string) (string, *failure) {
	file, problem := c.lockRun(ctx, name)
	if problem != nil {
		return "", problem
	}
	defer file.lock.Close()
	r, problem := file.load()
	if problem != nil {
		return "", problem
	}
	if r == nil {
		return "", fail("run_not_found", "No run with this name exists for the current site and key.")
	}
	if r.Task == nil {
		return "", fail("run_pending", "The run has no confirmed task ID yet. Repeat video create with the same spec and run.")
	}
	return r.Task.ID, nil
}
