package install

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

type targetFile struct {
	path    string
	body    []byte
	mode    os.FileMode
	profile bool // append the PATH stanza while preserving existing content and permissions
}

// A trust root is the chosen home or an explicitly configured user directory.
// Check every component beneath it before creating or replacing a file.
func ensureDir(roots []string, path string) error {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
			continue
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		dir := root
		for _, part := range append([]string{"."}, strings.Split(rel, string(filepath.Separator))...) {
			if part != "." {
				dir = filepath.Join(dir, part)
			}
			info, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) {
				if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
					return err
				}
				info, err = os.Lstat(dir)
			}
			if err != nil {
				return err
			}
			if info.Mode().Type() != os.ModeDir {
				return fmt.Errorf("expected a real directory at %s; check the linked or conflicting path", dir)
			}
		}
		return nil
	}
	return fmt.Errorf("destination %s is outside the configured installation directories", path)
}

func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file at %s; move the conflicting path before installing", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		f.Close()
		return nil, fmt.Errorf("file changed while opening %s", path)
	}
	return f, nil
}

func readRegular(path string) ([]byte, os.FileInfo, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	return data, info, err
}

type snapshot struct {
	info os.FileInfo
	sum  [sha256.Size]byte
}

func (s snapshot) matches(path string) (bool, error) {
	f, err := openRegular(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.info == nil, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || s.info == nil || !os.SameFile(s.info, info) || s.info.Mode() != info.Mode() || s.info.Size() != info.Size() {
		return false, err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	return bytes.Equal(s.sum[:], h.Sum(nil)), err
}

type change struct {
	targetFile
	dir                 string
	dirInfo             os.FileInfo
	old, next, previous snapshot
	applied, keep       bool
}

func applyFiles(roots []string, targets []targetFile, finish func() error) (backups []string, err error) {
	var changes []*change
	seen := make(map[string]targetFile)
	defer func() {
		for i := len(changes) - 1; i >= 0; i-- {
			c := changes[i]
			if err != nil && c.applied {
				if rollbackErr := c.undo(); rollbackErr != nil {
					c.keep = true
					err = errors.Join(err, fmt.Errorf("rollback %s (recovery directory %s): %w", c.path, c.dir, rollbackErr))
				}
			}
		}
		committed := err == nil
		for _, c := range changes {
			keep := c.old.info != nil && (c.keep || (committed && !c.profile))
			if keep {
				backups = append(backups, filepath.Join(c.dir, "previous"))
			}
			err = errors.Join(err, c.clean(keep))
		}
	}()
	for _, target := range targets {
		if err := ensureDir(roots, filepath.Dir(target.path)); err != nil {
			return nil, err
		}
		if target.profile {
			var err error
			target.path, err = profileDestination(roots[0], target.path)
			if err != nil {
				return nil, err
			}
		}
		key := pathKey(target.path)
		if prev, ok := seen[key]; ok {
			if prev.profile == target.profile && prev.mode == target.mode && bytes.Equal(prev.body, target.body) {
				continue
			}
			return nil, fmt.Errorf("conflicting installation destinations at %s", target.path)
		}
		seen[key] = target
		c, err := stage(target)
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, c)
		}
	}
	for _, c := range changes {
		if err := ensureDir(roots, filepath.Dir(c.path)); err != nil {
			return nil, err
		}
		if err := c.checkStage(); err != nil {
			return nil, err
		}
		staged := filepath.Join(c.dir, "next")
		if match, err := c.next.matches(staged); err != nil {
			return nil, err
		} else if !match {
			return nil, fmt.Errorf("staged file changed during installation: %s", staged)
		}
		if match, err := c.old.matches(c.path); err != nil {
			return nil, err
		} else if !match {
			return nil, fmt.Errorf("destination changed during installation: %s", c.path)
		}
		if err := fileops.Publish(staged, c.path, c.old.info != nil); err != nil {
			// A directory sync may fail after the file was published.
			c.applied, _ = c.next.matches(c.path)
			return nil, err
		}
		c.applied = true
	}
	if finish != nil {
		err = finish()
	}
	return nil, err
}

// Dotfile managers commonly symlink several shell profiles into one file.
// Update its regular target only within the same user's home and leave the
// links themselves in place. Explicit external config roots may host plain
// profiles, but do not delegate arbitrary linked writes.
func profileDestination(home, path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode()&os.ModeSymlink == 0) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realHome, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("shell profile %s links outside the user home to %s", path, target)
	}
	return filepath.Join(home, rel), nil
}

func stage(target targetFile) (*change, error) {
	old, info, err := readRegular(target.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	body, mode := target.body, target.mode
	if target.profile {
		if bytes.Contains(old, body) {
			return nil, nil
		}
		prefix := bytes.Clone(old)
		if len(prefix) != 0 && prefix[len(prefix)-1] != '\n' {
			prefix = append(prefix, '\n')
		}
		body = append(prefix, body...)
		if info != nil {
			mode = info.Mode().Perm()
		}
	}
	if info != nil && bytes.Equal(old, body) && (runtime.GOOS == "windows" || mode&0100 == 0 || info.Mode()&0100 != 0) {
		return nil, nil
	}
	dir, err := os.MkdirTemp(filepath.Dir(target.path), ".ifytv-install-*")
	if err != nil {
		return nil, err
	}
	c := &change{targetFile: target, dir: dir, old: snapshot{info, sha256.Sum256(old)}}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	c.dirInfo, err = f.Stat()
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	c.next, err = writeVerified(filepath.Join(dir, "next"), body, mode)
	if err == nil && info != nil {
		c.previous, err = writeVerified(filepath.Join(dir, "previous"), old, info.Mode().Perm())
	}
	if err != nil {
		return nil, errors.Join(err, c.clean(false))
	}
	return c, nil
}

func writeVerified(path string, body []byte, mode os.FileMode) (snapshot, error) {
	var saved snapshot
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return saved, err
	}
	if _, err = f.Write(body); err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		saved.info, err = f.Stat()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return saved, err
	}
	saved.sum = sha256.Sum256(body)
	if match, err := saved.matches(path); err != nil {
		return saved, err
	} else if !match {
		return saved, fmt.Errorf("failed to verify staged file %s", path)
	}
	return saved, nil
}

func (c *change) checkStage() error {
	info, err := os.Lstat(c.dir)
	if err != nil {
		return err
	}
	if info.Mode().Type() != os.ModeDir || !os.SameFile(c.dirInfo, info) {
		return fmt.Errorf("installation staging directory was replaced: %s", c.dir)
	}
	return nil
}

func (c *change) undo() error {
	if match, err := c.next.matches(c.path); err != nil {
		return err
	} else if !match {
		return errors.New("installed file has since been changed")
	}
	if c.old.info == nil {
		return os.Remove(c.path)
	}
	if err := c.checkStage(); err != nil {
		return err
	}
	previous := filepath.Join(c.dir, "previous")
	if match, err := c.previous.matches(previous); err != nil {
		return err
	} else if !match {
		return errors.New("the staged previous file has changed")
	}
	return fileops.Publish(previous, c.path, true)
}

func (c *change) clean(keep bool) error {
	if err := c.checkStage(); err != nil {
		return err
	}
	remove := func(name string) error {
		err := os.Remove(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	err := remove(filepath.Join(c.dir, "next"))
	if !keep {
		err = errors.Join(err, remove(filepath.Join(c.dir, "previous")), remove(c.dir))
	}
	return err
}
