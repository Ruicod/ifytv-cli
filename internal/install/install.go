// Package install installs the current CLI and its embedded Agent Skill offline.
package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
	"github.com/Ruicod/ifytv-cli/skills"
)

type Result struct {
	Executable string   `json:"executable"`
	Skills     []string `json:"skills"`
	Backups    []string `json:"backups,omitempty"`
	Portable   bool     `json:"portable,omitempty"`
}

func UserHome() (home string, portable bool, err error) {
	if value, ok := os.LookupEnv("IFYTV_INSTALL_HOME"); ok {
		if !filepath.IsAbs(value) {
			return "", true, errors.New("IFYTV_INSTALL_HOME requires an absolute directory")
		}
		return filepath.Clean(value), true, nil
	}
	home, err = os.UserHomeDir()
	if err == nil && !filepath.IsAbs(home) {
		err = errors.New("cannot resolve an absolute user home")
	}
	return filepath.Clean(home), false, err
}

func Run(home string, skillDirs []string, portable bool) (Result, error) {
	var result Result
	roots := []string{home}
	if !portable {
		for _, name := range []string{"CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME", "ZDOTDIR"} {
			if path := os.Getenv(name); filepath.IsAbs(path) {
				roots = append(roots, filepath.Clean(path))
			}
		}
	}
	lockDir := filepath.Join(home, ".local", "share", "ifytv-installer")
	if err := ensureDir(roots, lockDir); err != nil {
		return result, err
	}
	lock, err := lockInstall(filepath.Join(lockDir, "install.lock"))
	if err != nil {
		return result, err
	}
	defer lock.Close()

	name := "ifytv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	result.Executable = filepath.Join(home, ".local", "bin", name)
	result.Skills = skillDirs
	result.Portable = portable
	self, err := os.Executable()
	if err != nil {
		return result, err
	}
	// os.Executable can name a launcher symlink on some systems. Read the
	// resolved running program, never an executable selected from PATH.
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return result, err
	}
	program, _, err := readRegular(self)
	if err != nil {
		return result, err
	}
	files := []targetFile{{path: result.Executable, body: program, mode: 0755}}
	physical, err := physicalSkillDirs(roots, home, skillDirs)
	if err != nil {
		return result, err
	}
	for _, path := range skills.ResourcePaths() {
		body, err := skills.Files.ReadFile("ifytv/" + path)
		if err != nil {
			return result, err
		}
		for _, dir := range physical {
			files = append(files, targetFile{path: filepath.Join(dir, filepath.FromSlash(path)), body: body, mode: 0644})
		}
	}
	var finish func() error
	if !portable {
		profiles, persist, err := planPath(home)
		if err != nil {
			return result, err
		}
		files = append(files, profiles...)
		finish = persist
	}
	result.Backups, err = applyFiles(roots, files, finish)
	return result, err
}

func lockInstall(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		file, err = openRegular(path)
	}
	if err != nil {
		return nil, err
	}
	locked, err := fileops.TryLock(file)
	if err != nil || !locked {
		file.Close()
		if err == nil {
			err = errors.New("another IFYTV installation is running; retry when it finishes")
		}
		return nil, err
	}
	return file, nil
}

// Agents often link their Skill to the same canonical .agents installation.
// Follow a whole-Skill link only to another explicitly recognized Skill path.
func physicalSkillDirs(roots []string, home string, dirs []string) ([]string, error) {
	known := map[string]bool{pathKey(filepath.Join(home, ".agents", "skills", "ifytv")): true}
	for _, dir := range dirs {
		known[pathKey(dir)] = true
	}
	var result []string
	used := make(map[string]bool)
	for _, dir := range dirs {
		seen := make(map[string]bool)
		for {
			if err := ensureDir(roots, filepath.Dir(dir)); err != nil {
				return nil, err
			}
			key := pathKey(dir)
			info, err := os.Lstat(dir)
			if errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().Type() == os.ModeDir) {
				if !used[key] {
					result = append(result, dir)
					used[key] = true
				}
				break
			}
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink == 0 || seen[key] {
				return nil, fmt.Errorf("expected an Agent Skill directory at %s", dir)
			}
			seen[key] = true
			link, err := os.Readlink(dir)
			if err != nil {
				return nil, err
			}
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(dir), link)
			}
			if !known[pathKey(link)] {
				return nil, fmt.Errorf("Skill link %s points outside the selected IFYTV Skill directories to %s", dir, link)
			}
			dir = filepath.Clean(link)
		}
	}
	return result, nil
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
