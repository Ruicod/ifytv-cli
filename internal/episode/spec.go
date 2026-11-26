package episode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSpecBytes = 1 << 20
	maxEpisodeMS = 120_000
	maxShots     = 24
	maxCues      = 128
)

type episodeSpec struct {
	Ratio      string `json:"ratio"`
	Shots      []shot `json:"shots"`
	durationMS int
}

type shot struct {
	Video      string `json:"video"`
	DurationMS int    `json:"duration_ms"`
	Dialogue   []cue  `json:"dialogue,omitempty"`
}

type cue struct {
	Audio   string `json:"audio"`
	StartMS int    `json:"start_ms"`
	EndMS   int    `json:"end_ms"`
	Text    string `json:"text"`
}

func loadSpec(path string) (episodeSpec, error) {
	var spec episodeSpec
	path, err := localPath(".", path)
	if err != nil || !regularFile(path) {
		return spec, fail("invalid_episode_spec", "Use a readable local JSON episode spec.")
	}
	file, err := os.Open(path)
	if err != nil {
		return spec, fail("invalid_episode_spec", "Cannot open the episode spec.")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSpecBytes+1))
	if err != nil || len(data) > maxSpecBytes || !utf8.Valid(data) {
		return spec, fail("invalid_episode_spec", "Use a UTF-8 JSON episode spec of at most 1 MiB.")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&spec) != nil || decoder.Decode(new(any)) != io.EOF {
		return spec, fail("invalid_episode_spec", "Use one JSON object with ratio and shots.")
	}
	if err := spec.validateTimeline(); err != nil {
		return spec, err
	}
	return spec, spec.resolveMedia(filepath.Dir(path))
}

func (spec *episodeSpec) validateTimeline() error {
	if spec.Ratio != "9:16" && spec.Ratio != "16:9" {
		return fail("invalid_episode_spec", "Choose episode ratio 9:16 or 16:9.")
	}
	if len(spec.Shots) == 0 || len(spec.Shots) > maxShots {
		return fail("invalid_episode_spec", "Supply between 1 and 24 shots in playback order.")
	}
	count := 0
	for i := range spec.Shots {
		shot := &spec.Shots[i]
		if shot.DurationMS < 100 || shot.DurationMS > maxEpisodeMS-spec.durationMS {
			return fail("invalid_episode_spec", "Give each shot at least 100 ms and keep the episode at or below 120 seconds.")
		}
		spec.durationMS += shot.DurationMS
		count += len(shot.Dialogue)
		if count > maxCues {
			return fail("invalid_episode_spec", "The episode can contain at most 128 dialogue cues.")
		}
		lastEnd := 0
		for j := range shot.Dialogue {
			cue := &shot.Dialogue[j]
			if cue.StartMS < lastEnd || cue.EndMS <= cue.StartMS || cue.EndMS > shot.DurationMS {
				return fail("invalid_episode_spec", fmt.Sprintf("Shot %d requires ordered, non-overlapping dialogue windows within its duration.", i+1))
			}
			cue.Text = strings.Join(strings.Fields(cue.Text), " ")
			if cue.Text == "" || utf8.RuneCountInString(cue.Text) > 160 || strings.IndexFunc(cue.Text, unicode.IsControl) >= 0 {
				return fail("invalid_episode_spec", fmt.Sprintf("Shot %d dialogue %d needs 1 to 160 characters of subtitle text.", i+1, j+1))
			}
			lastEnd = cue.EndMS
		}
	}
	return nil
}

func (spec *episodeSpec) resolveMedia(dir string) error {
	for i := range spec.Shots {
		shot := &spec.Shots[i]
		video, err := localPath(dir, shot.Video)
		if err != nil || !strings.EqualFold(filepath.Ext(video), ".mp4") || !regularFile(video) {
			return fail("invalid_episode_media", fmt.Sprintf("Shot %d needs a regular local MP4 file.", i+1))
		}
		shot.Video = video
		for j := range shot.Dialogue {
			cue := &shot.Dialogue[j]
			audio, err := localPath(dir, cue.Audio)
			if err != nil || audioFormat(audio) == "" || !regularFile(audio) {
				return fail("invalid_episode_media", fmt.Sprintf("Shot %d dialogue %d needs a regular local MP3, WAV or M4A file.", i+1, j+1))
			}
			cue.Audio = audio
		}
	}
	return nil
}

func (spec episodeSpec) dimensions() (int, int) {
	if spec.Ratio == "9:16" {
		return 720, 1280
	}
	return 1280, 720
}

func localPath(dir, name string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.IndexByte(name, 0) >= 0 || strings.Contains(normalized, "://") || strings.HasPrefix(normalized, "//") {
		return "", fail("invalid_local_path", "Use a local filesystem path.")
	}
	if !filepath.IsAbs(name) {
		if filepath.VolumeName(name) != "" {
			return "", fail("invalid_local_path", "Use a local filesystem path.")
		}
		name = filepath.Join(dir, name)
	}
	path, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if filepath.Separator == '\\' {
		volume := filepath.VolumeName(path)
		relative := strings.TrimLeft(path[len(volume):], "\\/")
		if strings.HasPrefix(volume, "\\\\") || strings.Contains(relative, ":") || !filepath.IsLocal(relative) {
			return "", fail("invalid_local_path", "Use a local filesystem path.")
		}
	}
	return path, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func audioFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "mp3"
	case ".wav":
		return "wav"
	case ".m4a":
		return "mov"
	default:
		return ""
	}
}
