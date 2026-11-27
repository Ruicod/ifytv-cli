package episode

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestSubtitlesWrapAndPreserveLongUnbrokenText(t *testing.T) {
	cases := map[string]string{
		"latin":     strings.Repeat("W", 120),
		"chinese":   strings.Repeat("雨", 159) + "。",
		"combining": strings.Repeat("a\u0301", 70),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			spec := episodeSpec{Ratio: "9:16", Shots: []shot{{
				DurationMS: 1000,
				Dialogue:   []cue{{StartMS: 100, EndMS: 900, Text: text}},
			}}}
			const header = "1\n00:00:00,100 --> 00:00:00,900\n"
			subtitle := spec.subtitles()
			if !strings.HasPrefix(subtitle, header) {
				t.Fatalf("incorrect cue timestamp: %q", subtitle)
			}
			lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(subtitle, header), "\n\n"), "\n")
			if len(lines) < 2 || strings.Join(lines, "") != text {
				t.Fatalf("wrap changed or failed to split subtitle text: %q", lines)
			}
			for _, line := range lines {
				first, _ := utf8.DecodeRuneInString(line)
				if unicode.Is(unicode.Mn, first) {
					t.Fatalf("wrap separated a combining mark: %q", line)
				}
				if utf8.RuneCountInString(line) > 13 {
					t.Fatalf("portrait line too long: %q", line)
				}
			}
		})
	}
}

func TestExportRejectsConflictingDialogue(t *testing.T) {
	dir := t.TempDir()
	spec := writeSpec(t, dir, `{
        "ratio":"9:16",
        "shots":[{"video":"shot.mp4","duration_ms":1000,"dialogue":[
            {"audio":"one.mp3","start_ms":100,"end_ms":500,"text":"一"},
            {"audio":"two.wav","start_ms":300,"end_ms":700,"text":"二"}
        ]}]
    }`)
	out := filepath.Join(dir, "episode.mp4")
	_, err := Export(context.Background(), spec, out)
	requireError(t, err, "invalid_episode_spec")
	requireAbsent(t, out)
}

func TestExportPreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "episode.mp4")
	if err := os.WriteFile(out, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Export(context.Background(), "missing.json", out)
	requireError(t, err, "output_exists")
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "keep this" {
		t.Fatalf("existing file changed: %q, %v", got, err)
	}
}

func TestCheckMissingEncoder(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	requireError(t, Check(context.Background()), "ffmpeg_unavailable")
}

func TestExportBurnsCaptionsAndMixesOriginalDialogue(t *testing.T) {
	requireFFmpeg(t)
	dir := filepath.Join(t.TempDir(), "带 空格和'符号")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	makeVideo(t, dir, "red.mp4", "red")
	makeVideo(t, dir, "blue.mp4", "blue")
	tone(t, filepath.Join(dir, "one.wav"), 440, 130)
	tone(t, filepath.Join(dir, "two.wav"), 880, 170)
	ff(t, dir, "-i", "two.wav", "-c:a", "libmp3lame", "two.mp3")
	spec := writeSpec(t, dir, `{
        "ratio":"9:16",
        "shots":[
            {"video":"red.mp4","duration_ms":800,"dialogue":[
                {"audio":"one.wav","start_ms":200,"end_ms":500,"text":"字幕 FIRST"}
            ]},
            {"video":"blue.mp4","duration_ms":800,"dialogue":[
                {"audio":"two.mp3","start_ms":100,"end_ms":500,"text":"字幕 SECOND"}
            ]}
        ]
    }`)
	out := filepath.Join(dir, "成片.mp4")
	result, err := Export(context.Background(), spec, out)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != out || result.Shots != 2 || result.DurationMS != 1600 || result.Bytes < 1 {
		t.Fatalf("export receipt = %+v", result)
	}
	raw := cmd(t, dir, "ffprobe", "-v", "error", "-show_entries",
		"stream=codec_name,codec_type,width,height,duration:format=duration", "-of", "json", out)
	var probe struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			Duration  string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 2 || probe.Streams[0].CodecName != "h264" ||
		probe.Streams[0].Width != 720 || probe.Streams[0].Height != 1280 || probe.Streams[1].CodecName != "aac" {
		t.Fatalf("unexpected MP4 streams: %+v", probe.Streams)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || math.Abs(duration-1.6) > 0.034 {
		t.Fatalf("duration = %s (%v)", probe.Format.Duration, err)
	}
	frame := func(seconds string) []byte {
		return ff(t, dir, "-ss", seconds, "-i", out, "-frames:v", "1",
			"-vf", "scale=180:320", "-pix_fmt", "rgb24", "-threads", "1", "-f", "rawvideo", "pipe:1")
	}
	first := frame("0.3")
	second := frame("1.1")
	if len(first) != 180*320*3 || len(second) != len(first) {
		t.Fatalf("unexpected frame sizes: %d, %d", len(first), len(second))
	}
	pixel := (160*180 + 90) * 3
	if first[pixel] < 170 || first[pixel+2] > 20 || second[pixel] > 20 || second[pixel+2] < 170 {
		t.Fatalf("shot order: first %v second %v", first[pixel:pixel+3], second[pixel:pixel+3])
	}
	if whitePixels(first) <= whitePixels(frame("0.06"))+20 || whitePixels(second) <= 20 {
		t.Fatal("expected visible white subtitle pixels during both dialogue cues")
	}
	audio := ff(t, dir, "-i", out, "-map", "0:a:0", "-ac", "1", "-ar", "8000",
		"-c:a", "pcm_s16le", "-f", "s16le", "pipe:1")
	if silence := toneLevel(audio, 25, 100, 330); silence > 0.01 {
		t.Fatalf("original video audio leaked into episode: %f", silence)
	}
	if soon := toneLevel(audio, 380, 450, 440); soon > 0.01 {
		t.Fatalf("dialogue continued after source audio ended: %f", soon)
	}
	if a, b := toneLevel(audio, 230, 310, 440), toneLevel(audio, 940, 1040, 880); a < 0.08 || b < 0.08 {
		t.Fatalf("expected each voice at its timestamp, got %f and %f", a, b)
	}
}

func TestExportMakesHorizontalMP4WithoutDialogue(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	makeVideo(t, dir, "shot.mp4", "red")
	spec := writeSpec(t, dir, `{"ratio":"16:9","shots":[{"video":"shot.mp4","duration_ms":259}]}`)
	out := filepath.Join(dir, "episode.mp4")
	result, err := Export(context.Background(), spec, out)
	if err != nil {
		t.Fatal(err)
	}
	if result.DurationMS != 259 || result.Shots != 1 {
		t.Fatalf("unexpected export receipt: %+v", result)
	}
	probe := cmd(t, dir, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", out)
	if string(bytes.TrimSpace(probe)) != "1280x720" {
		t.Fatalf("frame size = %q", probe)
	}
}

func TestExportRejectsDialogueLongerThanItsWindow(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	makeVideo(t, dir, "shot.mp4", "red")
	tone(t, filepath.Join(dir, "speech.wav"), 440, 500)
	spec := writeSpec(t, dir, `{
        "ratio":"16:9",
        "shots":[{"video":"shot.mp4","duration_ms":800,"dialogue":[
            {"audio":"speech.wav","start_ms":100,"end_ms":200,"text":"Should not trim me"}
        ]}]
    }`)
	out := filepath.Join(dir, "episode.mp4")
	_, err := Export(context.Background(), spec, out)
	requireError(t, err, "dialogue_too_long")
	requireAbsent(t, out)
}

func TestExportRejectsShortVideo(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	makeVideo(t, dir, "shot.mp4", "red")
	out := filepath.Join(dir, "episode.mp4")
	spec := writeSpec(t, dir, `{"ratio":"16:9","shots":[{"video":"shot.mp4","duration_ms":1400}]}`)
	_, err := Export(context.Background(), spec, out)
	requireError(t, err, "shot_too_short")
	requireAbsent(t, out)
}

func writeSpec(t *testing.T, dir, spec string) string {
	t.Helper()
	path := filepath.Join(dir, "episode.json")
	if err := os.WriteFile(path, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireError(t *testing.T, err error, code string) {
	t.Helper()
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected file after failure: %s (%v)", path, err)
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if err := Check(context.Background()); err != nil {
		var problem *Error
		if errors.As(err, &problem) && (problem.Code == "ffmpeg_unavailable" || problem.Code == "ffmpeg_unsupported") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
}

func makeVideo(t *testing.T, dir, name, color string) {
	t.Helper()
	ff(t, dir, "-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:s=160x90:r=30:d=0.8", color),
		"-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=0.8",
		"-c:v", "libx264", "-c:a", "aac", "-pix_fmt", "yuv420p", "-threads", "1", "-shortest", name)
}

func ff(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	return cmd(t, dir, "ffmpeg", append([]string{"-hide_banner", "-v", "error", "-nostdin"}, args...)...)
}

func cmd(t *testing.T, dir, tool string, args ...string) []byte {
	t.Helper()
	command := exec.Command(tool, args...)
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	got, err := command.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", tool, args, err, stderr.String())
	}
	return got
}

func tone(t *testing.T, path string, hz, millis int) {
	t.Helper()
	const rate = 48000
	samples := rate * millis / 1000
	var buffer bytes.Buffer
	buffer.WriteString("RIFF")
	binary.Write(&buffer, binary.LittleEndian, uint32(36+2*samples))
	buffer.WriteString("WAVEfmt ")
	binary.Write(&buffer, binary.LittleEndian, []uint32{16})
	binary.Write(&buffer, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&buffer, binary.LittleEndian, []uint32{rate, rate * 2})
	binary.Write(&buffer, binary.LittleEndian, []uint16{2, 16})
	buffer.WriteString("data")
	binary.Write(&buffer, binary.LittleEndian, uint32(samples*2))
	for i := 0; i < samples; i++ {
		wave := math.Sin(float64(i*hz) * (2 * math.Pi / rate))
		binary.Write(&buffer, binary.LittleEndian, int16(16000*wave))
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func whitePixels(pixels []byte) int {
	var n int
	for i := 0; i+2 < len(pixels); i += 3 {
		if pixels[i] > 205 && pixels[i+1] > 205 && pixels[i+2] > 205 {
			n++
		}
	}
	return n
}

func toneLevel(audio []byte, start, end, hz int) float64 {
	const rate = 8000
	from, to := start*rate/1000, end*rate/1000
	var x, y float64
	for i := from; i < to && 2*i+1 < len(audio); i++ {
		sample := float64(int16(binary.LittleEndian.Uint16(audio[2*i:]))) / 32768
		phase := float64(i*hz) * (2 * math.Pi / rate)
		x += math.Cos(phase) * sample
		y += math.Sin(phase) * sample
	}
	return (2 * math.Hypot(x, y)) / float64(to-from)
}
