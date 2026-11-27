package ifytvcli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCanvasReadParsesPinnedProjectLink(t *testing.T) {
	t.Setenv("IFYTV_BASE_URL", "https://site.example/ifytv")
	project := "https://site.example/ifytv/canvas/cloud/" + projectID + "?node=existing-card"
	pin := digest([]byte("fixture device approval"))
	for _, args := range [][]string{
		{"canvas", "inspect", "--project", project, "--authorization-id", pin, "--out", "canvas.json"},
		{"canvas", "fetch", "--project", project, "--authorization-id", pin, "--asset", assetID, "--out", "line.mp3"},
	} {
		opts, problem := parseArgs(args)
		if problem != nil || opts.project != projectID || opts.authorizationID != pin || opts.out == "" {
			t.Fatalf("%s: %+v, %v", args[1], opts, problem)
		}
	}
	_, denied := parseArgs([]string{"canvas", "inspect", "--project", "https://another.example/canvas/cloud/" + projectID, "--authorization-id", pin})
	if denied == nil || denied.Code != "project_site_mismatch" {
		t.Fatalf("cross-site canvas link must be rejected, got %v", denied)
	}
}

func TestGuardedContinuationRequiresProjectAndDeviceApproval(t *testing.T) {
	base := []string{"video", "create", "--run", "episode-shot-02", "--spec", "shot.json"}
	pin := digest([]byte("fixture device approval"))
	for _, flags := range [][]string{
		{"--revision", "73", "--node", "manual-card"},
		{"--project", projectID, "--revision", "73", "--node", "manual-card"},
		{"--project", projectID, "--node", "manual-card", "--authorization-id", pin},
		{"--project", "auto", "--revision", "73", "--authorization-id", pin},
		{"--no-canvas", "--revision", "73", "--authorization-id", pin},
		{"--project", projectID, "--revision", "0", "--authorization-id", pin},
		{"--project", projectID, "--revision", "3", "--revision", "4", "--authorization-id", pin},
		{"--project", projectID, "--revision", "73", "--node", "../other-node", "--authorization-id", pin},
	} {
		if _, problem := parseArgs(append(base, flags...)); problem == nil {
			t.Fatalf("accepted unguarded continuation %v", flags)
		}
	}
	for _, target := range [][]string{nil, {"--node", "_manual-card:02"}} {
		flags := append([]string{"--project", projectID, "--revision", "73", "--authorization-id", pin}, target...)
		if opts, problem := parseArgs(append(base, flags...)); problem != nil || opts.project != projectID {
			t.Fatalf("valid guarded continuation rejected: %+v %v", opts, problem)
		}
	}
}

func TestCanvasSnapshotRedactionKeepsExactRevision(t *testing.T) {
	const revision = 18014398509481985
	var stdout bytes.Buffer
	input := output{Path: "file-fixture-secret-name", Canvas: &canvasSnapshot{Revision: revision}}
	if err := render(&stdout, io.Discard, input, true, "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	data := stdout.String()
	if strings.Contains(data, "fixture-secret") || !strings.Contains(data, "\"revision\":18014398509481985") {
		t.Fatalf("unexpected redacted snapshot %s", data)
	}
}
