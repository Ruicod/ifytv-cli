package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the public command in a separate module. The fixture needs no API
// credentials, dependencies, network, or edits to the real CLI checkout.
func TestReleaseCommand(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "release-tool")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", tool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build release command: %v\n%s", err, output)
	}

	repo := t.TempDir()
	writeTestFile(t, repo, "go.mod", "module github.com/Ruicod/ifytv-cli\n\ngo 1.22\n")
	writeTestFile(t, repo, "skills/embed.go", `package skills

import "embed"

//go:embed ifytv/SKILL.md ifytv/examples/h3.json
var Files embed.FS
`)
	writeTestFile(t, repo, "cmd/ifytv/main.go", `package main

import (
	"fmt"
	"os"
	"github.com/Ruicod/ifytv-cli/skills"
)

var version = "dev"

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	if os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	data, err := skills.Files.ReadFile(os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	os.Stdout.Write(data)
}
`)
	skill := "---\nname: ifytv\ndescription: Test fixture\n---\n# IFYTV\n"
	example := "{\n  \"model\": \"test\"\n}\n"
	writeTestFile(t, repo, "skills/ifytv/SKILL.md", skill)
	writeTestFile(t, repo, "skills/ifytv/examples/h3.json", example)
	writeTestFile(t, repo, "skills/ifytv/local-notes.txt", "exclude this unrelated file")
	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(tool, args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	pack := func(t *testing.T, out string) {
		t.Helper()
		if message, err := run(t, "-version", "0.1.0", "-out", out); err != nil {
			t.Fatalf("pack: %v\n%s", err, message)
		}
	}
	mustFail := func(t *testing.T, want string, args ...string) {
		t.Helper()
		output, err := run(t, args...)
		if err == nil || !strings.Contains(strings.ToLower(output), want) {
			t.Fatalf("%v: want failure mentioning %q, got %v\n%s", args, want, err, output)
		}
	}

	t.Run("only compiler inputs enter the source snapshot", func(t *testing.T) {
		files := map[string]bool{}
		if err := collectBuildInputs(repo, platform{"linux", "arm64"}, files); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{
			filepath.FromSlash("cmd/ifytv/main.go"):             true,
			filepath.FromSlash("skills/embed.go"):               true,
			filepath.FromSlash("skills/ifytv/SKILL.md"):         true,
			filepath.FromSlash("skills/ifytv/examples/h3.json"): true,
		}
		if !reflect.DeepEqual(files, want) {
			t.Fatalf("build inputs: got %v, want %v", files, want)
		}
	})

	t.Run("invalid arguments never create output", func(t *testing.T) {
		for _, version := range []string{"", "v0.1.0", "0.1", "0.01.0", "../0.1.0", `0.1.0\evil`, "0.1.0 -X", "0.1.0-01"} {
			out := filepath.Join(t.TempDir(), "releases")
			mustFail(t, "version", "-version", version, "-out", out)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid version %q created output or stat failed: %v", version, err)
			}
		}
		mustFail(t, "out", "-version", "0.1.0", "-out", "")
		mustFail(t, "argument", "-version", "0.1.0", "unexpected")
	})

	t.Run("ordered verifiable archives and immutable version", func(t *testing.T) {
		out := t.TempDir()
		pack(t, out)
		checkDirectory(t, out, "0.1.0", "latest.json")
		first := checkDistribution(t, out, skill, example)
		mustFail(t, "0.1.0", "-version", "0.1.0", "-out", out)
		if got := checkDistribution(t, out, skill, example); !bytes.Equal(got, first) {
			t.Fatal("retry modified the existing distribution")
		}

		// Release bytes must survive different checkout line endings and staging paths.
		crlfSkill := strings.ReplaceAll(skill, "\n", "\r\n")
		crlfExample := strings.ReplaceAll(example, "\n", "\r\n")
		writeTestFile(t, repo, "skills/ifytv/SKILL.md", crlfSkill)
		writeTestFile(t, repo, "skills/ifytv/examples/h3.json", crlfExample)
		secondOut := t.TempDir()
		pack(t, secondOut)
		if string(readTestFile(t, filepath.Join(repo, "skills/ifytv/SKILL.md"))) != crlfSkill ||
			string(readTestFile(t, filepath.Join(repo, "skills/ifytv/examples/h3.json"))) != crlfExample {
			t.Fatal("packing rewrote the checkout's CRLF resources")
		}
		if second := checkDistribution(t, secondOut, skill, example); !bytes.Equal(first, second) {
			t.Fatalf("repacking changed asset hashes\n%s\n%s", first, second)
		}
	})

	t.Run("late pointer failure preserves the previous entry", func(t *testing.T) {
		out := t.TempDir()
		writeTestFile(t, out, "latest.json/marker", "keep")
		mustFail(t, "latest", "-version", "0.1.0", "-out", out)
		checkDirectory(t, out, "latest.json")
		if got := string(readTestFile(t, filepath.Join(out, "latest.json", "marker"))); got != "keep" {
			t.Fatalf("latest entry changed: %q", got)
		}
		if err := os.Rename(filepath.Join(out, "latest.json"), filepath.Join(out, "saved-entry")); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, out, "latest.json", "{\"version\":\"0.0.9\"}\n")
		writeTestFile(t, out, "0.0.9/manifest.json", "previous manifest")
		pack(t, out)
		checkDirectory(t, out, "0.0.9", "0.1.0", "latest.json", "saved-entry")
		checkDistribution(t, out, skill, example)
		if got := string(readTestFile(t, filepath.Join(out, "0.0.9", "manifest.json"))); got != "previous manifest" {
			t.Fatalf("publishing a new version changed the previous manifest: %q", got)
		}
	})

	t.Run("invalid skill fails without publishing", func(t *testing.T) {
		out := t.TempDir()
		const name = "skills/ifytv/examples/h3.json"
		t.Cleanup(func() { writeTestFile(t, repo, name, example) })
		writeTestFile(t, repo, name, "{")
		mustFail(t, "h3.json", "-version", "0.1.0", "-out", out)
		checkDirectory(t, out)
	})

	t.Run("concurrent packers produce one complete version", func(t *testing.T) {
		out := t.TempDir()
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := run(t, "-version", "0.1.0", "-out", out)
				results <- err
			}()
		}
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Fatalf("expected one successful packer, got %v and %v", first, second)
		}
		checkDirectory(t, out, "0.1.0", "latest.json")
		checkDistribution(t, out, skill, example)
	})

	t.Run("missing linker version is caught on the host", func(t *testing.T) {
		target := runtime.GOOS + "/" + runtime.GOARCH
		switch target {
		case "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64":
		default:
			t.Skip("host is outside the release matrix")
		}
		out := t.TempDir()
		const name = "cmd/ifytv/main.go"
		original := string(readTestFile(t, filepath.Join(repo, name)))
		t.Cleanup(func() { writeTestFile(t, repo, name, original) })
		writeTestFile(t, repo, name, strings.Replace(original, "fmt.Println(version)", `fmt.Println("dev")`, 1))
		mustFail(t, "--version", "-version", "0.1.0", "-out", out)
		checkDirectory(t, out)
	})

	t.Run("failed cross-build cleans staging and preserves previous release", func(t *testing.T) {
		out := t.TempDir()
		previous := "{\"version\":\"0.0.9\"}\n"
		writeTestFile(t, out, "latest.json", previous)
		writeTestFile(t, out, "0.0.9/manifest.json", "previous manifest")
		writeTestFile(t, repo, "cmd/ifytv/broken_linux_arm64.go", "package main\nvar _ = missingForLinuxARM64\n")
		mustFail(t, "linux", "-version", "0.1.0", "-out", out)
		checkDirectory(t, out, "0.0.9", "latest.json")
		if got := string(readTestFile(t, filepath.Join(out, "latest.json"))); got != previous {
			t.Fatalf("latest changed after build failure: %q", got)
		}
		if got := string(readTestFile(t, filepath.Join(out, "0.0.9", "manifest.json"))); got != "previous manifest" {
			t.Fatalf("old manifest changed: %q", got)
		}
	})
}

func checkDistribution(t *testing.T, out, skill, example string) []byte {
	t.Helper()
	var latest map[string]string
	if err := json.Unmarshal(readTestFile(t, filepath.Join(out, "latest.json")), &latest); err != nil ||
		!reflect.DeepEqual(latest, map[string]string{"version": "0.1.0"}) {
		t.Fatalf("latest.json: %#v (%v)", latest, err)
	}
	dir := filepath.Join(out, "0.1.0")
	manifest := readTestFile(t, filepath.Join(dir, "manifest.json"))
	var doc struct {
		Version             string
		DeviceAuthorization bool `json:"device_authorization"`
		CanvasGeneration    bool `json:"canvas_generation"`
		Assets              []struct {
			Name, OS, Arch, URL, SHA256 string
			Bytes                       int64
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if !doc.DeviceAuthorization {
		t.Fatal("the packaged client must advertise browser device authorization")
	}
	if !doc.CanvasGeneration {
		t.Fatal("the packaged client must advertise in-place canvas generation")
	}
	targets := [][2]string{{"windows", "amd64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"skill", "any"}}
	if doc.Version != "0.1.0" || len(doc.Assets) != len(targets) {
		t.Fatalf("unexpected version %q or asset count %d", doc.Version, len(doc.Assets))
	}
	for i, target := range targets {
		asset := doc.Assets[i]
		name := "ifytv-0.1.0-" + target[0] + "-" + target[1] + ".zip"
		if target[0] == "skill" {
			name = "ifytv-skill-0.1.0.zip"
		}
		if asset.Name != name || asset.OS != target[0] || asset.Arch != target[1] ||
			asset.URL != "https://github.com/Ruicod/ifytv-cli/releases/download/v0.1.0/"+name {
			t.Fatalf("asset %d: %+v", i, asset)
		}
		body := readTestFile(t, filepath.Join(dir, name))
		hash := sha256.Sum256(body)
		if asset.Bytes != int64(len(body)) || asset.SHA256 != hex.EncodeToString(hash[:]) {
			t.Fatalf("size or hash mismatch: %+v", asset)
		}
		z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if target[0] == "skill" {
			if len(z.File) != 2 || z.File[0].Name != "ifytv/SKILL.md" || z.File[1].Name != "ifytv/examples/h3.json" {
				t.Fatalf("unexpected skill ZIP entries: %v", z.File)
			}
			if string(readEntry(t, z.File[0], 0644)) != skill || string(readEntry(t, z.File[1], 0644)) != example {
				t.Fatal("packaged skill text differs from the source")
			}
			continue
		}
		checkCLIZIP(t, z, target, skill, example)
	}
	for _, target := range [][2]string{{"windows", "arm64"}, {"linux", "arm64"}} {
		name := "ifytv-0.1.0-" + target[0] + "-" + target[1] + ".zip"
		body := readTestFile(t, filepath.Join(dir, name))
		z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		checkCLIZIP(t, z, target, skill, example)
	}
	checkInstallers(t, dir, doc.Version)
	return checkReleaseChecksums(t, dir, doc.Version)
}

func checkCLIZIP(t *testing.T, z *zip.Reader, target [2]string, skill, example string) {
	t.Helper()
	entry := "ifytv"
	if target[0] == "windows" {
		entry += ".exe"
	}
	if len(z.File) != 1 || z.File[0].Name != entry {
		t.Fatalf("unexpected %v ZIP entries: %v", target, z.File)
	}
	binary := readEntry(t, z.File[0], 0755)
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != target[0] || settings["GOARCH"] != target[1] || settings["CGO_ENABLED"] != "0" {
		t.Fatalf("wrong ABI for %v: %v", target, settings)
	}
	if target[0] == runtime.GOOS && target[1] == runtime.GOARCH {
		program := filepath.Join(t.TempDir(), entry)
		if err := os.WriteFile(program, binary, 0755); err != nil {
			t.Fatal(err)
		}
		version, err := exec.Command(program, "--version").Output()
		if err != nil || strings.TrimSpace(string(version)) != "0.1.0" {
			t.Fatalf("installed --version: %q (%v)", version, err)
		}
		for name, want := range map[string]string{"ifytv/SKILL.md": skill, "ifytv/examples/h3.json": example} {
			got, err := exec.Command(program, name).Output()
			if err != nil || string(got) != want {
				t.Fatalf("embedded %s differs from the canonical Skill: got %q, want %q (%v)", name, got, want, err)
			}
		}
	}
}

func readEntry(t *testing.T, f *zip.File, mode os.FileMode) []byte {
	t.Helper()
	if !f.Mode().IsRegular() || f.Mode().Perm() != mode || !f.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected metadata for %s: %v %v", f.Name, f.Mode(), f.Modified)
	}
	r, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r) // Reading through EOF also verifies CRC32.
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func checkDirectory(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s entries: got %v, want %v", dir, got, want)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
