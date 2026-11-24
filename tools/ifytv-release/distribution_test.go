package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func checkReleaseChecksums(t *testing.T, dir, version string) []byte {
	t.Helper()
	names := []string{
		"ifytv-" + version + "-darwin-amd64.zip", "ifytv-" + version + "-darwin-arm64.zip",
		"ifytv-" + version + "-linux-amd64.zip", "ifytv-" + version + "-linux-arm64.zip",
		"ifytv-" + version + "-windows-amd64.zip", "ifytv-" + version + "-windows-arm64.zip",
		"ifytv-install.tgz", "ifytv-skill-" + version + ".zip", "install.ps1", "install.sh", "manifest.json",
	}
	checkDirectory(t, dir, append([]string{"SHA256SUMS"}, names...)...)
	var want strings.Builder
	for _, name := range names {
		body := readTestFile(t, filepath.Join(dir, name))
		fmt.Fprintf(&want, "%x  %s\n", sha256.Sum256(body), name)
	}
	got := readTestFile(t, filepath.Join(dir, "SHA256SUMS"))
	if string(got) != want.String() {
		t.Fatalf("unexpected SHA256SUMS\ngot:\n%s\nwant:\n%s", got, want.String())
	}
	return got
}

func checkInstallers(t *testing.T, dir, version string) {
	t.Helper()
	scripts := map[string][]byte{}
	for name, targets := range map[string][]string{
		"install.sh":  {"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"},
		"install.ps1": {"windows-amd64", "windows-arm64"},
	} {
		body := readTestFile(t, filepath.Join(dir, name))
		if bytes.Contains(body, []byte("@@")) || bytes.Contains(body, []byte("\r")) ||
			!bytes.Contains(body, []byte(version)) ||
			!bytes.Contains(body, []byte("https://github.com/Ruicod/ifytv-cli/releases/download/v")) {
			t.Fatalf("%s is not pinned to release %s or contains template markers/CRLF", name, version)
		}
		for _, target := range targets {
			archive := readTestFile(t, filepath.Join(dir, "ifytv-"+version+"-"+target+".zip"))
			digest := fmt.Sprintf("%x", sha256.Sum256(archive))
			if !bytes.Contains(body, []byte(digest)) {
				t.Fatalf("%s is missing the %s ZIP digest", name, target)
			}
		}
		scripts["package/"+name] = body
	}

	bundle := readTestFile(t, filepath.Join(dir, "ifytv-install.tgz"))
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	if gz.Name != "" || gz.Comment != "" || !gz.ModTime.IsZero() {
		t.Fatalf("unexpected gzip metadata: %+v", gz.Header)
	}
	entries := tar.NewReader(gz)
	modes := map[string]int64{
		"package/package.json": 0644, "package/install.cjs": 0755,
		"package/install.sh": 0755, "package/install.ps1": 0644,
	}
	for {
		header, err := entries.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		mode, ok := modes[header.Name]
		if !ok || header.Typeflag != tar.TypeReg || header.Mode != mode || header.ModTime.Unix() != 0 ||
			header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" {
			t.Fatalf("unexpected npm archive member or metadata: %+v", header)
		}
		delete(modes, header.Name)
		body, err := io.ReadAll(entries)
		if err != nil {
			t.Fatal(err)
		}
		switch header.Name {
		case "package/package.json":
			var pkg struct {
				Name, Version                                                                      string
				Bin, Engines                                                                       map[string]string
				Scripts, Dependencies, OptionalDependencies, PeerDependencies, BundledDependencies json.RawMessage
			}
			if err := json.Unmarshal(body, &pkg); err != nil {
				t.Fatal(err)
			}
			if pkg.Name != "ifytv-install" || pkg.Version != version || len(pkg.Bin) != 1 ||
				strings.TrimPrefix(pkg.Bin["ifytv-install"], "./") != "install.cjs" || pkg.Engines["node"] != ">=16" ||
				pkg.Scripts != nil || pkg.Dependencies != nil || pkg.OptionalDependencies != nil ||
				pkg.PeerDependencies != nil || pkg.BundledDependencies != nil {
				t.Fatalf("unexpected npm metadata: %s", body)
			}
		case "package/install.cjs":
			if !bytes.HasPrefix(body, []byte("#!/usr/bin/env node\n")) || bytes.Contains(body, []byte("@@")) || bytes.Contains(body, []byte("\r")) {
				t.Fatal("npm entry point is missing its shebang or contains unresolved markers/CRLF")
			}
		default:
			if !bytes.Equal(body, scripts[header.Name]) {
				t.Fatalf("%s differs from its standalone installer", header.Name)
			}
		}
	}
	if len(modes) != 0 {
		t.Fatalf("missing npm archive members: %v", modes)
	}
	// Drain gzip after the tar terminator to verify the outer stream's checksum.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapTemplatePins(t *testing.T) {
	const template = "v@@VERSION@@\r\n@@SHA256_WINDOWS_AMD64@@\r\n@@SHA256_WINDOWS_ARM64@@\r\n"
	x64 := strings.Repeat("a", 64)
	arm64 := strings.Repeat("1", 64)
	pins := map[platform]string{{"windows", "amd64"}: x64, {"windows", "arm64"}: arm64}
	release := "0.2.0-rc.5"
	body, err := renderBootstrap("install.ps1", template, release, pins)
	if err != nil || string(body) != "v"+release+"\n"+x64+"\n"+arm64+"\n" {
		t.Fatalf("rendered installer %q (%v)", body, err)
	}
	for _, tt := range []struct {
		name   string
		source string
		pins   map[platform]string
	}{
		{"missing version marker", strings.ReplaceAll(template, "@@VERSION@@", "latest"), pins},
		{"missing architecture marker", strings.ReplaceAll(template, "@@SHA256_WINDOWS_ARM64@@", x64), pins},
		{"unknown marker", template + "@@TYPO@@", pins},
		{"no archive hash", template, map[platform]string{{"windows", "amd64"}: x64}},
		{"invalid archive hash", template, map[platform]string{{"windows", "amd64"}: x64, {"windows", "arm64"}: strings.Repeat("x", 64)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if body, err := renderBootstrap("install.ps1", tt.source, release, tt.pins); err == nil {
				t.Fatalf("accepted an unpinned bootstrap: %s", body)
			}
		})
	}
}
