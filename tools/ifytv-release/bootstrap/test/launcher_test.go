package bootstrap_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const version = "1.2.3-rc.4"

// The Windows fixture ZIP contains this test executable. Record the native
// arguments received by the verified executable without touching a real home.
func TestMain(m *testing.M) {
	if os.Getenv("IFYTV_TEST_EXECUTABLE") == "1" && len(os.Args) > 1 && os.Args[1] == "install" {
		home := os.Getenv("IFYTV_INSTALL_HOME")
		if home == "" || os.WriteFile(filepath.Join(home, "args"), []byte(strings.Join(os.Args[1:], "\n")+"\n"), 0600) != nil {
			os.Exit(99)
		}
		code, _ := strconv.Atoi(os.Getenv("IFYTV_TEST_EXIT"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestShellBootstrap(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		if git, err := exec.LookPath("git"); err == nil {
			sh = filepath.Join(filepath.Dir(filepath.Dir(git)), "usr", "bin", "sh.exe")
		}
	}
	if _, err := os.Stat(sh); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	zipPath, digest := fixtureZIP(t, "ifytv", []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$IFYTV_INSTALL_HOME/args\"\nexit \"${IFYTV_TEST_EXIT:-0}\"\n"))
	cases := []struct {
		name, system, arch, target string
		translated, bad, fallback  bool
		python                     bool
		childExit                  int
	}{
		{name: "Linux x64", system: "Linux", arch: "x86_64", target: "linux-amd64"},
		{name: "Linux arm64 fallback tools", system: "Linux", arch: "aarch64", target: "linux-arm64", fallback: true},
		{name: "GNU tar and Python only", system: "Linux", arch: "x86_64", target: "linux-amd64", python: true},
		{name: "Intel Mac", system: "Darwin", arch: "x86_64", target: "darwin-amd64"},
		{name: "Apple Silicon", system: "Darwin", arch: "arm64", target: "darwin-arm64"},
		{name: "Rosetta", system: "Darwin", arch: "x86_64", target: "darwin-arm64", translated: true},
		{name: "mismatched digest", system: "Linux", arch: "x86_64", target: "linux-amd64", bad: true},
		{name: "unsupported architecture", system: "Linux", arch: "i686"},
		{name: "unsupported OS", system: "FreeBSD", arch: "amd64"},
		{name: "native install failure", system: "Linux", arch: "arm64", target: "linux-arm64", childExit: 17},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			python := ""
			if tc.python {
				name := "python3"
				if runtime.GOOS == "windows" {
					name = "python"
				}
				python, err = exec.LookPath(name)
				if err != nil {
					t.Skip("Python 3 unavailable")
				}
			}
			home := t.TempDir()
			tmp := filepath.Join(home, "staging with spaces")
			if err := os.Mkdir(tmp, 0700); err != nil {
				t.Fatal(err)
			}
			hash := digest
			if tc.bad {
				hash = strings.Repeat("0", 64)
			}
			installer := render(t, "install.sh", tc.target, hash)
			write(t, filepath.Join(home, "install.sh"), installer)
			driver := filepath.Join(home, "shell-fixture.sh")
			write(t, driver, strings.ReplaceAll(string(read(t, "shell-fixture.sh")), "\r\n", "\n"))
			cmd := exec.Command(sh, driver, "--agent", "claude", "one with spaces")
			cmd.Env = append(os.Environ(), "IFYTV_INSTALL_HOME="+filepath.ToSlash(home), "TMPDIR="+filepath.ToSlash(tmp),
				"IFYTV_TEST_ZIP="+filepath.ToSlash(zipPath), "IFYTV_TEST_OS="+tc.system, "IFYTV_TEST_ARCH="+tc.arch,
				"IFYTV_TEST_TRANSLATED="+strconv.FormatBool(tc.translated), "IFYTV_TEST_FALLBACK="+strconv.FormatBool(tc.fallback),
				"IFYTV_TEST_EXIT="+strconv.Itoa(tc.childExit), "IFYTV_TEST_PYTHON="+filepath.ToSlash(python))
			output, err := cmd.CombinedOutput()
			verify(t, home, tmp, tc.target, tc.bad, tc.childExit, "install\n--agent\nclaude\none with spaces\n", output, err)
		})
	}
}

func TestPowerShellBootstrap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable fixture")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	zipPath, digest := fixtureZIP(t, "ifytv.exe", read(t, executable))
	for _, shell := range []string{"powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			host, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(shell + " unavailable")
			}
			for _, tc := range []struct {
				name, machine, target string
				pipe, bad             bool
				childExit             int
			}{
				{name: "from disk", machine: "AMD64", target: "windows-amd64"},
				{name: "piped default", machine: "ARM64", target: "windows-arm64", pipe: true},
				{name: "wrong digest", machine: "AMD64", target: "windows-amd64", pipe: true, bad: true},
				{name: "unsupported processor", machine: "x86", pipe: true},
				{name: "native failure on disk", machine: "AMD64", target: "windows-amd64", childExit: 17},
				{name: "native failure in pipeline", machine: "AMD64", target: "windows-amd64", pipe: true, childExit: 17},
			} {
				t.Run(tc.name, func(t *testing.T) {
					home := t.TempDir()
					tmp := filepath.Join(home, "staging with spaces")
					if err := os.Mkdir(tmp, 0700); err != nil {
						t.Fatal(err)
					}
					hash := digest
					if tc.bad {
						hash = strings.Repeat("0", 64)
					}
					write(t, filepath.Join(home, "install.ps1"), render(t, "install.ps1", tc.target, hash))
					cmd := exec.Command(host, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "powershell-fixture.ps1")
					cmd.Env = append(os.Environ(), "IFYTV_INSTALL_HOME="+home, "TEMP="+tmp, "TMP="+tmp, "IFYTV_TEST_ARCH="+tc.machine,
						"IFYTV_TEST_ZIP="+zipPath, "IFYTV_TEST_EXECUTABLE=1", "IFYTV_TEST_PIPE="+strconv.FormatBool(tc.pipe),
						"IFYTV_TEST_EXIT="+strconv.Itoa(tc.childExit), "PROCESSOR_ARCHITECTURE=AMD64", "PROCESSOR_ARCHITEW6432=")
					output, err := cmd.CombinedOutput()
					wantArgs := "install\n--agent\ncodex\none with spaces\n"
					if tc.pipe {
						wantArgs = "install\n"
						if _, err := os.Stat(filepath.Join(home, "continued")); err != nil {
							t.Fatalf("calling shell did not survive the pipeline: %v\n%s", err, output)
						}
					}
					verify(t, home, tmp, tc.target, tc.bad, tc.childExit, wantArgs, output, err)
				})
			}
		})
	}
}

func TestPackageLauncher(t *testing.T) {
	runtimes := []string{"node", "bun"}
	for _, name := range runtimes {
		t.Run(name, func(t *testing.T) {
			engine, err := exec.LookPath(name)
			if err != nil {
				t.Skip(name + " unavailable")
			}
			home := t.TempDir()
			wrapper := filepath.Join(home, "install.cjs")
			write(t, wrapper, string(read(t, "../install.cjs")))
			// Yarn PnP can put package sources inside a ZIP. Native processes
			// cannot see the adjacent launcher which the JS filesystem can read.
			hook := filepath.Join(home, "virtual-package.cjs")
			write(t, hook, "const fs = require('fs');\nconst original = fs.readFileSync;\n"+
				"fs.readFileSync = function (p, ...args) {\n"+
				"  if (/[/\\\\]install\\.(?:ps1|sh)$/.test(p)) return original(process.env.IFYTV_TEST_SCRIPT, ...args);\n"+
				"  return original(p, ...args);\n};\n")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$IFYTV_INSTALL_HOME/args\"\nprintf '%s' \"$0\" > \"$IFYTV_INSTALL_HOME/script\"\nexit 17\n"
			if runtime.GOOS == "windows" {
				script = "[IO.File]::WriteAllLines((Join-Path $env:IFYTV_INSTALL_HOME 'args'), [string[]]$args)\n" +
					"[IO.File]::WriteAllText((Join-Path $env:IFYTV_INSTALL_HOME 'script'), $PSCommandPath)\nexit 17\n"
			}
			embedded := filepath.Join(home, "embedded")
			write(t, embedded, script)
			cmd := exec.Command(engine, "--require", hook, wrapper, "--agent", "claude", "one with spaces")
			cmd.Env = append(os.Environ(), "IFYTV_INSTALL_HOME="+home, "IFYTV_TEST_SCRIPT="+embedded)
			out, err := cmd.CombinedOutput()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 17 {
				t.Fatalf("expected installer exit 17, got %v\n%s", err, out)
			}
			checkText(t, filepath.Join(home, "args"), "--agent\nclaude\none with spaces\n")
			temporary := string(read(t, filepath.Join(home, "script")))
			if _, err := os.Stat(filepath.Dir(temporary)); !os.IsNotExist(err) {
				t.Fatalf("temporary launcher directory still exists: %q, %v", temporary, err)
			}
		})
	}
}

func verify(t *testing.T, home, tmp, target string, bad bool, childExit int, args string, output []byte, err error) {
	t.Helper()
	wantCode := childExit
	if bad || target == "" {
		wantCode = 1
	}
	gotCode := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			gotCode = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if gotCode != wantCode {
		t.Fatalf("exit %d, want %d\n%s", gotCode, wantCode, output)
	}
	if target == "" {
		if _, err := os.Stat(filepath.Join(home, "url")); !os.IsNotExist(err) {
			t.Fatalf("attempted download on unsupported platform: %v", err)
		}
	} else {
		checkText(t, filepath.Join(home, "url"), "https://github.com/Ruicod/ifytv-cli/releases/download/v"+version+"/ifytv-"+version+"-"+target+".zip")
	}
	if bad || target == "" {
		if _, err := os.Stat(filepath.Join(home, "args")); !os.IsNotExist(err) {
			t.Fatalf("unexpected execution: %v", err)
		}
		message := strings.ToLower(string(output))
		if (bad && !strings.Contains(message, "sha-256")) || (target == "" && !strings.Contains(message, "unsupported")) {
			t.Fatalf("missing error diagnostic: %s", output)
		}
	} else {
		checkText(t, filepath.Join(home, "args"), args)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging was not cleaned: %v %v", entries, err)
	}
}

func render(t *testing.T, name, target, digest string) string {
	t.Helper()
	s := strings.ReplaceAll(string(read(t, filepath.Join("..", name))), "@@VERSION@@", version)
	for _, platform := range []string{"windows-amd64", "windows-arm64", "darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"} {
		hash := strings.Repeat("f", 64)
		if platform == target {
			hash = digest
		}
		key := strings.ToUpper(strings.ReplaceAll(platform, "-", "_"))
		s = strings.ReplaceAll(s, "@@SHA256_"+key+"@@", hash)
	}
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func fixtureZIP(t *testing.T, entry string, executable []byte) (string, string) {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	f, err := w.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "download.zip")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path, fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func checkText(t *testing.T, path, want string) {
	t.Helper()
	if got := strings.ReplaceAll(string(read(t, path)), "\r\n", "\n"); got != want {
		t.Fatalf("%s: got %q, want %q", path, got, want)
	}
}
