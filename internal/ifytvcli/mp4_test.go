package ifytvcli

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Synthetic FFmpeg fixtures: silence, and a 32x24 blue H.264 video with AAC.
//
//go:embed testdata/audio-only.mp4
var audioOnlyMP4 []byte

//go:embed testdata/fragmented.mp4
var fragmentedMP4 []byte

//go:embed testdata/video.mp4
var mp4Fixture []byte

//go:embed testdata/faststart.mp4
var faststartMP4 []byte

// Edit one field of a real FFmpeg box, retaining the correct HTTP length.
func mp4Field(t *testing.T, source []byte, kind string, occurrence, offset int, value uint32) []byte {
	t.Helper()
	data := bytes.Clone(source)
	pos := -4
	for i := 0; i < occurrence; i++ {
		next := bytes.Index(data[pos+4:], []byte(kind))
		if next < 0 {
			t.Fatalf("fixture missing %s #%d", kind, occurrence)
		}
		pos += 4 + next
	}
	binary.BigEndian.PutUint32(data[pos+offset:], value)
	return data
}

func TestDownloadRejectsInvalidMP4(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"ftyp and mdat without moov", []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp42\x00\x00\x00\x0cmdatdata")},
		{"audio only", audioOnlyMP4},
		{"partial trailing box", append(bytes.Clone(mp4Fixture), 0, 0, 0)},
		{"video chunk outside mdat", mp4Field(t, mp4Fixture, "stco", 1, 12, 0)},
		{"video sample exceeds mdat", mp4Field(t, mp4Fixture, "stsz", 1, 16, 1<<20)},
		{"video sample refers to external data", mp4Field(t, mp4Fixture, "url ", 1, 4, 0)},
		{"empty video data", mp4Field(t, mp4Fixture, "mdat", 1, 0, 0x66726565)}, // mdat becomes free
		{"AVC data has invalid NAL length", mp4Field(t, mp4Fixture, "mdat", 1, 25, 0)},
		{"AVC configuration has truncated parameter set", mp4Field(t, mp4Fixture, "avcC", 1, 10, 0xffffffff)},
		{"fragment initialization without samples", fragmentedMP4[:bytes.Index(fragmentedMP4, []byte("moof"))-4]},
		{"late fragment points into metadata", mp4Field(t, fragmentedMP4, "trun", 3, 12, 0)},
		{"fragment references unknown track", mp4Field(t, fragmentedMP4, "tfhd", 1, 8, 99)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.data)))
				w.Write(tc.data)
			})
			dir := t.TempDir()
			reply := invoke(context.Background(), "video", "download", "--task", taskID, "--out", filepath.Join(dir, "clip.mp4"), "--json").result(t, false)
			if reply["error"].(map[string]any)["code"] != "invalid_download" {
				t.Fatalf("unexpected download error: %v", reply)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
				t.Fatalf("files left after failed download: %v (%v)", files, err)
			}
		})
	}
}

func TestMP4CompactSampleSizesAnd64BitChunkOffsets(t *testing.T) {
	words := func(values ...uint32) []byte {
		var b []byte
		for _, value := range values {
			b = binary.BigEndian.AppendUint32(b, value)
		}
		return b
	}
	for _, tc := range []struct {
		name string
		bits uint32
		data []byte
	}{
		{"4-bit odd count", 4, []byte{0x32, 0x40}},
		{"8-bit", 8, []byte{3, 2, 4}},
		{"16-bit", 16, []byte{0, 3, 0, 2, 0, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tables := mp4Boxes{
				{"stz2", append(words(0, tc.bits, 3), tc.data...)},
				{"stsc", words(0, 1, 1, 3, 1)},
				{"stts", words(0, 1, 3, 1000)},
				{"co64", words(0, 1, 0, 8)},
			}
			f := mp4File{ctx: context.Background(), size: 17, media: []mp4Location{{0, 8, 17}}, workLeft: 10}
			descriptions := []mp4Description{{video: true, local: true}}
			if !f.chunks(tables, descriptions) || !f.hasVideo {
				t.Fatal("valid compact samples rejected")
			}
			f.media[0].end--
			if f.chunks(tables, descriptions) {
				t.Fatal("last sample extends beyond mdat")
			}
		})
	}
}

func TestDownloadAcceptsMP4Layouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"moov after media", mp4Fixture},
		{"faststart", faststartMP4},
		{"fragmented AVC and AAC", fragmentedMP4},
		{"terminal mdat extends to EOF", mp4Field(t, faststartMP4, "mdat", 1, -4, 0)},
		{"64-bit trailing box", append(bytes.Clone(mp4Fixture), []byte("\x00\x00\x00\x01free\x00\x00\x00\x00\x00\x00\x00\x10")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testSite(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", strconv.Itoa(len(tc.data)))
				w.Write(tc.data)
			})
			path := filepath.Join(t.TempDir(), "clip.mp4")
			invoke(context.Background(), "video", "download", "--task", taskID, "--out", path, "--json").result(t, true)
			if body, err := os.ReadFile(path); err != nil || !bytes.Equal(body, tc.data) {
				t.Fatalf("published video differs: %v", err)
			}
		})
	}
}
