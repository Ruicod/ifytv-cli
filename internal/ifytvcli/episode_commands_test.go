package ifytvcli

import (
	"context"
	"path/filepath"
	"testing"
)

func TestEpisodeCheckUsesLocalToolsWithoutAccount(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("IFYTV_BASE_URL", "invalid site URL")
	t.Setenv("IFYTV_API_KEY", "invalid video key")
	result := invoke(context.Background(), "episode", "check", "--json").result(t, false)
	expectError(t, result, "ffmpeg_unavailable")
}

func TestEpisodeExportRequiresSpecAndOutput(t *testing.T) {
	for _, flags := range [][]string{{"--spec", "episode.json"}, {"--out", "episode.mp4"}} {
		args := append([]string{"episode", "export", "--json"}, flags...)
		result := invoke(context.Background(), args...).result(t, false)
		expectError(t, result, "usage")
	}
}

func TestMediaCommandsRequirePinnedAuthorization(t *testing.T) {
	commands := [][]string{
		{"media", "capabilities"},
		{"media", "generate", "--project", projectID, "--spec", "story.json", "--run", "pilot-img-01", "--out", filepath.Join(t.TempDir(), "shot.png")},
		{"media", "fetch", "--project", projectID, "--task", taskID, "--kind", "image", "--out", "frame.png"},
		{"media", "publish", "--project", projectID, "--run", "pilot-final", "--file", "final.mp4"},
	}
	for _, args := range commands {
		result := invoke(context.Background(), append(args, "--json")...).result(t, false)
		expectError(t, result, "usage")
	}
}
