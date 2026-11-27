package episode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

type binaries struct {
	ffmpeg  string
	ffprobe string
}

// Check verifies the local media tools before starting production tasks.
func Check(ctx context.Context) error {
	_, err := checkTools(ctx)
	return err
}

func checkTools(ctx context.Context) (binaries, error) {
	if err := interrupted(ctx); err != nil {
		return binaries{}, err
	}
	ffmpeg, encoderErr := exec.LookPath("ffmpeg")
	ffprobe, probeErr := exec.LookPath("ffprobe")
	unavailable := fail("ffmpeg_unavailable", "Install FFmpeg and FFprobe and make both available on PATH.")
	if encoderErr != nil || probeErr != nil {
		return binaries{}, unavailable
	}
	if err := exec.CommandContext(ctx, ffprobe, "-version").Run(); err != nil {
		if interruption := interrupted(ctx); interruption != nil {
			return binaries{}, interruption
		}
		return binaries{}, unavailable
	}
	for _, list := range []struct {
		option string
		names  []string
	}{
		{"-encoders", []string{"libx264", "aac", "pcm_s16le"}},
		{"-filters", []string{"subtitles", "tpad", "concat"}},
	} {
		raw, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", list.option).Output()
		if err != nil {
			if interruption := interrupted(ctx); interruption != nil {
				return binaries{}, interruption
			}
			return binaries{}, unavailable
		}
		names := make(map[string]bool)
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				names[fields[1]] = true
			}
		}
		for _, name := range list.names {
			if !names[name] {
				return binaries{}, fail("ffmpeg_unsupported", "Install an FFmpeg build with H.264 (libx264), AAC and the subtitles filter (libass).")
			}
		}
	}
	return binaries{ffmpeg: ffmpeg, ffprobe: ffprobe}, nil
}

type mediaStream struct {
	CodecName   string `json:"codec_name"`
	CodecType   string `json:"codec_type"`
	Duration    string `json:"duration"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Disposition struct {
		AttachedPicture int `json:"attached_pic"`
	} `json:"disposition"`
}

func (stream mediaStream) duration() float64 {
	seconds, err := strconv.ParseFloat(stream.Duration, 64)
	if err != nil || seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return seconds
}

func (tools binaries) probe(ctx context.Context, path string, videoOnly bool) ([]mediaStream, error) {
	args := []string{"-v", "error"}
	if videoOnly {
		args = append(args, "-select_streams", "v:0")
	}
	args = append(args, "-show_entries", "stream=codec_name,codec_type,duration,width,height:stream_disposition=attached_pic", "-of", "json")
	args = append(args, localInput("mov", path)...)
	raw, err := exec.CommandContext(ctx, tools.ffprobe, args...).Output()
	if err != nil {
		return nil, err
	}
	var probe struct {
		Streams []mediaStream `json:"streams"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	return probe.Streams, nil
}

const framesPerSecond = 30

func (tools binaries) validateShots(ctx context.Context, spec episodeSpec) error {
	for i, shot := range spec.Shots {
		streams, err := tools.probe(ctx, shot.Video, true)
		if interruption := interrupted(ctx); interruption != nil {
			return interruption
		}
		if err != nil || len(streams) != 1 || streams[0].CodecType != "video" ||
			streams[0].Disposition.AttachedPicture != 0 || streams[0].duration() == 0 {
			return fail("invalid_episode_media", fmt.Sprintf("Shot %d has no readable timed MP4 video stream.", i+1))
		}
		// The encoded clip can be one frame shorter than its nominal duration.
		if streams[0].duration()+1.0/framesPerSecond < float64(shot.DurationMS)/1000 {
			return fail("shot_too_short", fmt.Sprintf("Shot %d video is shorter than its requested duration.", i+1))
		}
	}
	return nil
}

func (tools binaries) verifyMP4(ctx context.Context, spec episodeSpec, path string) error {
	streams, err := tools.probe(ctx, path, false)
	if interruption := interrupted(ctx); interruption != nil {
		return interruption
	}
	width, height := spec.dimensions()
	duration := float64(spec.durationMS) / 1000
	if err != nil || len(streams) != 2 || streams[0].CodecName != "h264" ||
		streams[0].Width != width || streams[0].Height != height || streams[1].CodecName != "aac" ||
		math.Abs(streams[0].duration()-duration) > 1.0/framesPerSecond+0.001 ||
		math.Abs(streams[1].duration()-duration) > 0.024 {
		return fail("episode_export_failed", "The render did not produce a complete H.264 and AAC episode at the requested duration.")
	}
	return nil
}

func localInput(format, path string) []string {
	args := []string{"-protocol_whitelist", "file", "-f", format}
	if format == "mov" {
		args = append(args, "-enable_drefs", "0")
	}
	return append(args, "-i", path)
}

type dialogueWriter struct {
	target    io.Writer
	remaining int64
	tooLong   bool
	writeErr  error
}

func (writer *dialogueWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		writer.tooLong = true
		return 0, fail("dialogue_too_long", "The decoded dialogue exceeds its time window.")
	}
	n, err := writer.target.Write(data)
	writer.remaining -= int64(n)
	writer.writeErr = err
	return n, err
}

func (tools binaries) decodeDialogue(ctx context.Context, file io.Writer, cue cue, shotIndex, cueIndex int) (int64, error) {
	window := int64(cue.EndMS-cue.StartMS) * pcmBytesPerMS
	output := &dialogueWriter{target: file, remaining: window}
	args := append(encoderArgs(), localInput(audioFormat(cue.Audio), cue.Audio)...)
	args = append(args, "-map", "0:a:0", "-c:a", "pcm_s16le", "-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels), "-f", "s16le", "pipe:1")
	cmd := exec.CommandContext(ctx, tools.ffmpeg, args...)
	cmd.Stdout = output
	err := cmd.Run()
	if interruption := interrupted(ctx); interruption != nil {
		return 0, interruption
	}
	if output.tooLong {
		return 0, fail("dialogue_too_long", fmt.Sprintf("Shot %d dialogue %d exceeds its time window. Use a longer window or shorter source audio.", shotIndex+1, cueIndex+1))
	}
	if output.writeErr != nil {
		return 0, fail("output_unwritable", "Cannot write the decoded dialogue.")
	}
	if err != nil || output.remaining == window {
		return 0, fail("invalid_episode_media", fmt.Sprintf("Cannot decode the source audio for shot %d dialogue %d.", shotIndex+1, cueIndex+1))
	}
	return window - output.remaining, nil
}

func encoderArgs() []string {
	return []string{"-hide_banner", "-v", "error", "-nostdin", "-xerror"}
}

type captionWarnings struct {
	tail         string
	missingGlyph bool
}

func (warnings *captionWarnings) Write(data []byte) (int, error) {
	const marker = "failed to find any fallback with glyph"
	if !warnings.missingGlyph {
		text := warnings.tail + string(data)
		warnings.missingGlyph = strings.Contains(text, marker)
		// FFmpeg warnings can be split across writes.
		warnings.tail = text[max(0, len(text)-len(marker)+1):]
	}
	return len(data), nil
}

func (tools binaries) render(ctx context.Context, spec episodeSpec, dir, voiceTrack, captions, rendered string, hasSubtitles bool) error {
	args := encoderArgs()
	args = append(args, "-v", "warning", "-filter_complex_threads", "2")
	for _, shot := range spec.Shots {
		args = append(args, "-threads", "2")
		args = append(args, localInput("mov", shot.Video)...)
	}
	args = append(args, "-ar", strconv.Itoa(sampleRate), "-ac", strconv.Itoa(channels))
	args = append(args, localInput("s16le", voiceTrack)...)
	args = append(args, "-filter_complex", spec.videoFilter(captions, hasSubtitles), "-map", "[picture]",
		"-map", fmt.Sprintf("%d:a:0", len(spec.Shots)), "-t", fmt.Sprintf("%.3f", float64(spec.durationMS)/1000),
		"-c:v", "libx264", "-threads", "2", "-preset", "fast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-r", strconv.Itoa(framesPerSecond), "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart",
		"-f", "mp4", "-n", rendered)

	cmd := exec.CommandContext(ctx, tools.ffmpeg, args...)
	cmd.Dir = dir
	var warnings captionWarnings
	cmd.Stderr = &warnings
	if err := cmd.Run(); err != nil {
		if interruption := interrupted(ctx); interruption != nil {
			return interruption
		}
		return fail("episode_export_failed", "FFmpeg could not render the complete episode. Check the source files, available disk space and subtitle fonts.")
	}
	if warnings.missingGlyph {
		return fail("subtitle_font_missing", "Install a font that supports the subtitle language, then retry the export.")
	}
	return nil
}

func (spec episodeSpec) videoFilter(captions string, hasSubtitles bool) string {
	var graph strings.Builder
	width, height := spec.dimensions()
	var elapsedMS, previousFrame int
	for i, shot := range spec.Shots {
		elapsedMS += shot.DurationMS
		// Round the cumulative boundaries so short clips cannot compound a
		// frame of drift relative to the sample-accurate dialogue track.
		endFrame := (elapsedMS*framesPerSecond + 500) / 1000
		frames := endFrame - previousFrame
		previousFrame = endFrame
		fmt.Fprintf(&graph, "[%d:v:0]setpts=PTS-STARTPTS,fps=%d,"+
			"scale=w='max(2,trunc(min(%d,%d*dar)/2)*2)':h='max(2,trunc(min(%d,%d/dar)/2)*2)',"+
			"setsar=1,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:black,format=yuv420p,"+
			"tpad=stop=3:stop_mode=clone,trim=end_frame=%d,setpts=PTS-STARTPTS[v%d];",
			i, framesPerSecond, width, height, height, width, width, height, frames, i)
	}
	for i := range spec.Shots {
		fmt.Fprintf(&graph, "[v%d]", i)
	}
	fmt.Fprintf(&graph, "concat=n=%d:v=1:a=0,settb=1/%d,setpts=N", len(spec.Shots), framesPerSecond)
	if hasSubtitles {
		fmt.Fprintf(&graph, ",subtitles=filename=%s:force_style='FontSize=12,Outline=1,MarginV=12,WrapStyle=0'", captions)
	}
	graph.WriteString("[picture]")
	return graph.String()
}
