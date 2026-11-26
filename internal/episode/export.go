// Package episode exports a local episode from ordered shots and timed dialogue.
package episode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

// Error describes an episode export or preflight failure.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func fail(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

func interrupted(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fail("timeout", "Episode export timed out. Retry the same export.")
	}
	if ctx.Err() != nil {
		return fail("canceled", "Episode export was interrupted. Retry the same export.")
	}
	return nil
}

// Result describes the completed MP4 and its requested playback timeline.
type Result struct {
	Path       string
	Bytes      int64
	DurationMS int
	Shots      int
}

// Export combines local MP4 shots, source dialogue and burned subtitles. It
// writes the output only after the complete render has been verified.
func Export(ctx context.Context, specPath, outPath string) (Result, error) {
	var result Result
	if err := interrupted(ctx); err != nil {
		return result, err
	}
	path, err := localPath(".", outPath)
	if err != nil || !strings.EqualFold(filepath.Ext(path), ".mp4") {
		return result, fail("invalid_output", "Choose a local .mp4 output path.")
	}
	if _, err := os.Lstat(path); err == nil {
		return result, existingOutput()
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fail("invalid_output", "Cannot inspect the episode output path.")
	}
	spec, err := loadSpec(specPath)
	if err != nil {
		return result, err
	}
	tools, err := checkTools(ctx)
	if err != nil {
		return result, err
	}
	if err := tools.validateShots(ctx, spec); err != nil {
		return result, err
	}

	dir, err := os.MkdirTemp(filepath.Dir(path), ".ifytv-episode-")
	if err != nil {
		return result, fail("output_unwritable", "Cannot create a temporary directory beside the episode output.")
	}
	defer os.Remove(dir)
	const voiceTrack = "dialogue.pcm"
	const captions = "captions.srt"
	const rendered = "episode.mp4"
	voicePath := filepath.Join(dir, voiceTrack)
	captionsPath := filepath.Join(dir, captions)
	renderedPath := filepath.Join(dir, rendered)
	defer os.Remove(voicePath)
	defer os.Remove(captionsPath)
	defer os.Remove(renderedPath)

	if err := tools.mixDialogue(ctx, spec, voicePath); err != nil {
		return result, err
	}
	subtitleText := spec.subtitles()
	if len(subtitleText) > 0 {
		if err := os.WriteFile(captionsPath, []byte(subtitleText), 0600); err != nil {
			return result, fail("output_unwritable", "Cannot write the temporary subtitles.")
		}
	}
	if err := tools.render(ctx, spec, dir, voiceTrack, captions, rendered, len(subtitleText) > 0); err != nil {
		return result, err
	}
	if err := tools.verifyMP4(ctx, spec, renderedPath); err != nil {
		return result, err
	}
	size, err := syncOutput(renderedPath)
	if err != nil || size < 1 {
		return result, fail("output_unwritable", "Cannot save the complete rendered episode.")
	}
	if err := interrupted(ctx); err != nil {
		return result, err
	}
	if err := fileops.Publish(renderedPath, path, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			return result, existingOutput()
		}
		return result, fail("output_unwritable", "Cannot publish the rendered episode in the output directory.")
	}
	return Result{Path: path, Bytes: size, DurationMS: spec.durationMS, Shots: len(spec.Shots)}, nil
}

func existingOutput() *Error {
	return fail("output_exists", "The output already exists. Choose another --out path.")
}

func syncOutput(path string) (int64, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	info, err := file.Stat()
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return 0, err
	}
	return info.Size(), closeErr
}

const (
	sampleRate    = 48_000
	channels      = 2
	pcmBytesPerMS = sampleRate / 1000 * channels * 2
)

func (tools binaries) mixDialogue(ctx context.Context, spec episodeSpec, path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fail("output_unwritable", "Cannot create the episode dialogue track.")
	}
	defer file.Close()
	var offsetMS int
	var position int64
	for i, shot := range spec.Shots {
		for j, cue := range shot.Dialogue {
			start := int64(offsetMS+cue.StartMS) * pcmBytesPerMS
			if err := addSilence(file, start-position); err != nil {
				return fail("output_unwritable", "Cannot write the episode dialogue track.")
			}
			length, err := tools.decodeDialogue(ctx, file, cue, i, j)
			if err != nil {
				return err
			}
			position = start + length
		}
		offsetMS += shot.DurationMS
	}
	if err := addSilence(file, int64(spec.durationMS)*pcmBytesPerMS-position); err != nil {
		return fail("output_unwritable", "Cannot finish the episode dialogue track.")
	}
	if err := file.Close(); err != nil {
		return fail("output_unwritable", "Cannot save the episode dialogue track.")
	}
	return nil
}

func addSilence(file io.Writer, n int64) error {
	var quiet [16 * 1024]byte
	for n > 0 {
		length := min(n, int64(len(quiet)))
		if _, err := file.Write(quiet[:length]); err != nil {
			return err
		}
		n -= length
	}
	return nil
}

func (spec episodeSpec) subtitles() string {
	var out strings.Builder
	var offset, number int
	// Base the line budget on wide glyphs and leave room for the outline.
	columns := 28
	if spec.Ratio == "9:16" {
		columns = 11
	}
	// SubRip can interpret HTML and ASS formatting. Keep punctuation literal
	// when the cue text uses those delimiters.
	plainText := strings.NewReplacer("<", "＜", ">", "＞", "{", "｛", "}", "｝", "\\", "＼")
	for _, shot := range spec.Shots {
		for _, cue := range shot.Dialogue {
			number++
			fmt.Fprintf(&out, "%d\n%s --> %s\n%s\n\n", number,
				timestamp(offset+cue.StartMS), timestamp(offset+cue.EndMS), wrapSubtitle(plainText.Replace(cue.Text), columns))
		}
		offset += shot.DurationMS
	}
	return out.String()
}

func wrapSubtitle(text string, columns int) string {
	runes := []rune(text)
	if len(runes) <= columns {
		return text
	}
	var lines strings.Builder
	for len(runes) > columns {
		// Keep a nearby word break when possible. Long words and scripts that
		// do not use spaces still need a hard break to fit the frame.
		end := columns
		for i := columns; i >= columns-2; i-- {
			if runes[i] == ' ' {
				end = i + 1
				break
			}
		}
		for end < len(runes) {
			next := runes[end]
			if !unicode.IsMark(next) && next != '\u200d' && runes[end-1] != '\u200d' &&
				(next < '\U0001f3fb' || next > '\U0001f3ff') {
				break
			}
			end++
		}
		lines.WriteString(string(runes[:end]))
		runes = runes[end:]
		if len(runes) > 0 {
			lines.WriteByte('\n')
		}
	}
	lines.WriteString(string(runes))
	return lines.String()
}
func timestamp(ms int) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}
