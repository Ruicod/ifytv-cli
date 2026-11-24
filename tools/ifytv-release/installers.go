package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed bootstrap/install.sh bootstrap/install.ps1 bootstrap/install.cjs
var bootstrapTemplates embed.FS

func writeInstallers(dir, version string, digests map[platform]string) error {
	metadata := fmt.Sprintf(`{
  "name": "ifytv-install",
  "version": %q,
  "bin": { "ifytv-install": "install.cjs" },
  "engines": { "node": ">=16" }
}
`, version)
	entries := []archiveEntry{{"package.json", 0644, []byte(metadata)}}
	for _, script := range []struct {
		name string
		mode os.FileMode
	}{{"install.cjs", 0755}, {"install.sh", 0755}, {"install.ps1", 0644}} {
		source, err := bootstrapTemplates.ReadFile("bootstrap/" + script.name)
		if err != nil {
			return err
		}
		body, err := renderBootstrap(script.name, string(source), version, digests)
		if err != nil {
			return err
		}
		if script.name != "install.cjs" {
			if err := os.WriteFile(filepath.Join(dir, script.name), body, script.mode); err != nil {
				return err
			}
		}
		entries = append(entries, archiveEntry{script.name, script.mode, body})
	}
	return writeNPMArchive(filepath.Join(dir, "ifytv-install.tgz"), entries)
}

func renderBootstrap(name, source, version string, digests map[platform]string) ([]byte, error) {
	if name != "install.cjs" && !strings.Contains(source, "@@VERSION@@") {
		return nil, fmt.Errorf("%s: missing @@VERSION@@", name)
	}
	replacements := []string{"\r\n", "\n", "@@VERSION@@", version}
	for _, target := range platforms {
		nativeInstaller := "install.sh"
		if target.os == "windows" {
			nativeInstaller = "install.ps1"
		}
		if name != nativeInstaller {
			continue
		}
		marker := "@@SHA256_" + strings.ToUpper(target.os+"_"+target.arch) + "@@"
		if !strings.Contains(source, marker) {
			return nil, fmt.Errorf("%s: missing %s", name, marker)
		}
		digest := digests[target]
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("%s: missing or invalid SHA-256 for %s/%s", name, target.os, target.arch)
		}
		replacements = append(replacements, marker, digest)
	}
	body := strings.NewReplacer(replacements...).Replace(source)
	if strings.Contains(body, "@@") {
		return nil, fmt.Errorf("%s: unresolved template marker", name)
	}
	return []byte(body), nil
}

func writeNPMArchive(path string, entries []archiveEntry) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	gz := gzip.NewWriter(f)
	defer func() { err = errors.Join(err, gz.Close()) }()
	w := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, w.Close()) }()
	for _, entry := range entries {
		header := &tar.Header{
			Name: "package/" + entry.name, Typeflag: tar.TypeReg, Mode: int64(entry.mode.Perm()),
			Size: int64(len(entry.body)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
		}
		if err := w.WriteHeader(header); err != nil {
			return err
		}
		if _, err := w.Write(entry.body); err != nil {
			return err
		}
	}
	return nil
}
