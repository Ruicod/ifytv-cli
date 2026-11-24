package install

import (
	"errors"
	"os"
	"path/filepath"
)

const shPath = `# IFYTV CLI
case "${PATH-}" in
  "$HOME/.local/bin"|"$HOME/.local/bin":*) ;;
  *:"$HOME/.local/bin":*) export PATH="$HOME/.local/bin:${PATH%%:"$HOME/.local/bin":*}:${PATH#*:"$HOME/.local/bin":}" ;;
  *:"$HOME/.local/bin") export PATH="$HOME/.local/bin:${PATH%:"$HOME/.local/bin"}" ;;
  *) export PATH="$HOME/.local/bin${PATH:+:$PATH}" ;;
esac
`

const fishPath = `# IFYTV CLI
if test "$PATH[1]" != "$HOME/.local/bin"
    set -l ifytv_dirs $PATH
    set -l ifytv_pos (contains -i -- "$HOME/.local/bin" $ifytv_dirs)
    if set -q ifytv_pos[1]
        set -e ifytv_dirs[$ifytv_pos]
    end
    set -gx PATH "$HOME/.local/bin" $ifytv_dirs
end
`

func shellProfiles(home, shell, config, zdot string) ([]targetFile, error) {
	stanza := shPath
	var paths []string
	switch filepath.Base(shell) {
	case "fish":
		if !filepath.IsAbs(config) {
			config = filepath.Join(home, ".config")
		}
		paths = []string{filepath.Join(config, "fish", "conf.d", "ifytv-path.fish")}
		stanza = fishPath
	case "zsh":
		if zdot == "" {
			zdot = home
		} else if !filepath.IsAbs(zdot) {
			return nil, errors.New("use an absolute ZDOTDIR to configure the zsh PATH")
		}
		paths = []string{filepath.Join(zdot, ".zprofile"), filepath.Join(zdot, ".zshrc")}
	case "bash":
		// A login bash reads only the first existing file in this list.
		login := filepath.Join(home, ".profile")
		for _, file := range []string{".bash_profile", ".bash_login"} {
			path := filepath.Join(home, file)
			if _, err := os.Lstat(path); err == nil {
				login = path
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		paths = []string{login, filepath.Join(home, ".bashrc")}
	default:
		paths = []string{filepath.Join(home, ".profile")}
	}
	var files []targetFile
	for _, path := range paths {
		files = append(files, targetFile{path: path, body: []byte(stanza), mode: 0644, profile: true})
	}
	return files, nil
}
