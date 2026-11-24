//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package install

import (
	"errors"
	"os"
	"runtime"
	"strings"
)

func planPath(home string) ([]targetFile, func() error, error) {
	if strings.Contains(home, ":") {
		return nil, nil, errors.New("the user home contains the PATH separator ':'; use IFYTV_INSTALL_HOME for a portable install")
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
		if runtime.GOOS == "darwin" {
			shell = "zsh"
		}
	}
	files, err := shellProfiles(home, shell, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("ZDOTDIR"))
	return files, nil, err
}
