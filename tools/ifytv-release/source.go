package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// Compile from a snapshot so go:embed and the standalone ZIP receive identical
// bytes even on Go 1.22, which reads embed resources directly from disk.
func stageBuildSource(stage string, skill []archiveEntry) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	files := map[string]bool{"go.mod": true}
	for _, name := range []string{"go.sum", filepath.Join("cmd", "ifytv", "default.pgo")} {
		if _, err := os.Stat(name); err == nil {
			files[name] = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	for _, target := range platforms {
		if err := collectBuildInputs(root, target, files); err != nil {
			return "", fmt.Errorf("list %s/%s inputs: %w", target.os, target.arch, err)
		}
	}
	canonical := make(map[string][]byte, len(skill))
	for _, entry := range skill {
		canonical[filepath.Join("skills", filepath.FromSlash(entry.name))] = entry.body
	}
	source := filepath.Join(stage, "source")
	for name := range files {
		body, ok := canonical[name]
		if !ok {
			body, err = os.ReadFile(filepath.Join(root, name))
			if err != nil {
				return "", err
			}
		}
		dst := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, body, 0644); err != nil {
			return "", err
		}
	}
	return source, nil
}

func collectBuildInputs(root string, target platform, files map[string]bool) error {
	const fields = "Dir,Module,GoFiles,CgoFiles,CFiles,CXXFiles,MFiles,HFiles,FFiles,SFiles,SwigFiles,SwigCXXFiles,SysoFiles,EmbedFiles"
	cmd := targetGoCommand(target, "list", "-buildvcs=false", "-mod=readonly", "-deps", "-json="+fields, "./cmd/ifytv")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list: %w\n%s", err, stderr.Bytes())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg struct {
			Dir    string
			Module *struct{ Main bool }

			GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles []string
			SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles                      []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if pkg.Module == nil || !pkg.Module.Main {
			continue
		}
		dir, err := filepath.Rel(root, pkg.Dir)
		if err != nil {
			return err
		}
		for _, name := range slices.Concat(pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles,
			pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles) {
			rel := filepath.Join(dir, name)
			if !filepath.IsLocal(rel) {
				return fmt.Errorf("module input outside the checkout: %s", rel)
			}
			files[rel] = true
		}
	}
}

func targetGoCommand(target platform, args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local",
		"GOFLAGS=", "GOEXPERIMENT=", "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch,
		"GOAMD64=v1", "GOARM64=v8.0")
	return cmd
}
