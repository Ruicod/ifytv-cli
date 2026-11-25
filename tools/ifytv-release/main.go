// Command ifytv-release builds a local, versioned IFYTV release from the repository root.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const releaseURL = "https://github.com/Ruicod/ifytv-cli/releases/download/v"

// SemVer identifiers are also safe as a single filename and linker argument.
const number = `(0|[1-9][0-9]*)`
const preID = "(" + number + `|[0-9]*[A-Za-z-][A-Za-z0-9-]*)`
const buildID = `[A-Za-z0-9-]+`
const prerelease = "(-" + preID + `(\.` + preID + ")*)?"
const metadata = `(\+` + buildID + `(\.` + buildID + ")*)?"

var validVersion = regexp.MustCompile("^" + number + `\.` + number + `\.` + number + prerelease + metadata + "$")

type platform struct{ os, arch string }

// The website's manifest reader requires these four entries in their existing order, then the Skill.
var manifestPlatforms = []platform{
	{"windows", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"},
}

var platforms = slices.Concat(manifestPlatforms, []platform{{"windows", "arm64"}, {"linux", "arm64"}})

type asset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Version                string  `json:"version"`
	DeviceAuthorization    bool    `json:"device_authorization"`
	CanvasGeneration       bool    `json:"canvas_generation"`
	CanvasProjectCreation  bool    `json:"canvas_project_creation"`
	PersonalCanvasCreation bool    `json:"personal_canvas_creation"`
	Assets                 []asset `json:"assets"`
}

type archiveEntry struct {
	name string
	mode os.FileMode
	body []byte
}

func main() {
	version := flag.String("version", "", "release version without the v tag prefix")
	out := flag.String("out", "./dist/cli-releases", "local release directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := pack(*out, *version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(filepath.Join(*out, *version, "manifest.json"))
}

func pack(out, version string) (err error) {
	if !validVersion.MatchString(version) {
		return fmt.Errorf("invalid -version %q (expected e.g. 0.1.0)", version)
	}
	if strings.TrimSpace(out) == "" {
		return errors.New("-out requires a directory")
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	final := filepath.Join(out, version)
	if _, err := os.Lstat(final); err == nil {
		return fmt.Errorf("release %s already exists at %s", version, final)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	// Keep all intermediates on the destination filesystem for atomic renames.
	stage, err := os.MkdirTemp(out, ".ifytv-release-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(stage)) }()
	release := filepath.Join(stage, "release")
	if err := os.Mkdir(release, 0755); err != nil {
		return err
	}
	skill, err := readSkill()
	if err != nil {
		return err
	}
	source, err := stageBuildSource(stage, skill)
	if err != nil {
		return err
	}
	doc := manifest{
		Version: version, DeviceAuthorization: true, CanvasGeneration: true,
		CanvasProjectCreation: true, PersonalCanvasCreation: true,
	}
	digests := make(map[platform]string, len(platforms))
	for _, target := range platforms {
		program, err := buildCLI(stage, source, version, target)
		if err != nil {
			return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
		}
		name := fmt.Sprintf("ifytv-%s-%s-%s.zip", version, target.os, target.arch)
		a, err := makeAsset(release, version, name, target, []archiveEntry{program})
		if err != nil {
			return err
		}
		digests[target] = a.SHA256
		if slices.Contains(manifestPlatforms, target) {
			doc.Assets = append(doc.Assets, a)
		}
	}
	a, err := makeAsset(release, version, "ifytv-skill-"+version+".zip", platform{"skill", "any"}, skill)
	if err != nil {
		return err
	}
	doc.Assets = append(doc.Assets, a)
	if err := writeJSON(filepath.Join(release, "manifest.json"), doc); err != nil {
		return err
	}
	if err := writeInstallers(release, version, digests); err != nil {
		return err
	}
	if err := writeChecksums(release); err != nil {
		return err
	}
	latest := filepath.Join(stage, "latest.json")
	if err := writeJSON(latest, map[string]string{"version": version}); err != nil {
		return err
	}
	if err := os.Rename(release, final); err != nil {
		return fmt.Errorf("install version directory: %w", err)
	}
	if err := os.Rename(latest, filepath.Join(out, "latest.json")); err != nil {
		// Restore retryability without touching an earlier version or pointer.
		if rollbackErr := os.Rename(final, release); rollbackErr != nil {
			return fmt.Errorf("update latest.json: %w (returning %s to staging: %v)", err, final, rollbackErr)
		}
		return fmt.Errorf("update latest.json: %w", err)
	}
	return nil
}

func buildCLI(stage, source, version string, target platform) (archiveEntry, error) {
	name := "ifytv"
	if target.os == "windows" {
		name += ".exe"
	}
	path := filepath.Join(stage, name)
	cmd := targetGoCommand(target, "build", "-trimpath", "-buildvcs=false", "-mod=readonly",
		"-ldflags", "-buildid= -X main.version="+version, "-o", path, "./cmd/ifytv")
	cmd.Dir = source
	if output, err := cmd.CombinedOutput(); err != nil {
		return archiveEntry{}, fmt.Errorf("go build: %w\n%s", err, bytes.TrimSpace(output))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return archiveEntry{}, err
	}
	info, err := buildinfo.Read(bytes.NewReader(body))
	if err != nil {
		return archiveEntry{}, fmt.Errorf("read Go executable metadata: %w", err)
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != target.os || settings["GOARCH"] != target.arch || settings["CGO_ENABLED"] != "0" {
		return archiveEntry{}, fmt.Errorf("unexpected executable target %s/%s (CGO_ENABLED=%s)",
			settings["GOOS"], settings["GOARCH"], settings["CGO_ENABLED"])
	}
	if target.os == runtime.GOOS && target.arch == runtime.GOARCH {
		check := exec.Command(path, "--version")
		check.Env = append(os.Environ(), "IFYTV_BASE_URL=", "IFYTV_API_KEY=")
		output, err := check.Output()
		if err != nil {
			return archiveEntry{}, fmt.Errorf("check --version: %w", err)
		}
		if strings.TrimSpace(string(output)) != version {
			return archiveEntry{}, fmt.Errorf("--version returned %q, expected %s", bytes.TrimSpace(output), version)
		}
	}
	return archiveEntry{name, 0755, body}, nil
}

func readSkill() ([]archiveEntry, error) {
	var entries []archiveEntry
	// Explicit inputs keep local notes, credentials and example media out of the ZIP.
	for _, name := range []string{"ifytv/SKILL.md", "ifytv/examples/h3.json"} {
		body, err := os.ReadFile(filepath.Join("skills", filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(body)) == 0 || (strings.HasSuffix(name, ".json") && !json.Valid(body)) {
			return nil, fmt.Errorf("empty or invalid skill resource: %s", name)
		}
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		entries = append(entries, archiveEntry{name, 0644, body})
	}
	return entries, nil
}

func makeAsset(dir, version, name string, target platform, entries []archiveEntry) (asset, error) {
	path := filepath.Join(dir, name)
	if err := writeZIP(path, entries); err != nil {
		return asset{}, fmt.Errorf("write %s: %w", name, err)
	}
	// Hash the closed file, including the ZIP central directory.
	size, digest, err := hashFile(path)
	if err != nil {
		return asset{}, err
	}
	return asset{Name: name, OS: target.os, Arch: target.arch, URL: releaseURL + version + "/" + name,
		Bytes: size, SHA256: digest}, nil
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

func writeChecksums(dir string) error {
	// ReadDir lists the staged files in filename order, before SHA256SUMS exists.
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var sums strings.Builder
	for _, file := range files {
		_, digest, err := hashFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", digest, file.Name())
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
}

func writeZIP(path string, entries []archiveEntry) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := zip.NewWriter(f)
	defer func() { err = errors.Join(err, w.Close()) }()
	for _, entry := range entries {
		h := zip.FileHeader{Name: entry.name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(entry.mode)
		dst, err := w.CreateHeader(&h)
		if err != nil {
			return err
		}
		if _, err := dst.Write(entry.body); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
