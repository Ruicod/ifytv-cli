package ifytvcli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// The spec uses the gateway field names. Only the reference values change
// between the user's spec and the immutable create request.
type videoSpec struct {
	Model         string   `json:"model"`
	Mode          string   `json:"mode"`
	Prompt        string   `json:"prompt"`
	Duration      int      `json:"duration"`
	Ratio         string   `json:"ratio"`
	Resolution    string   `json:"resolution,omitempty"`
	Images        []string `json:"images,omitempty"`
	VideoURL      string   `json:"video_url,omitempty"`
	Videos        []string `json:"videos,omitempty"`
	Audio         []string `json:"audio,omitempty"`
	FaceCheck     *bool    `json:"face_check,omitempty"`
	GenerateAudio *bool    `json:"generate_audio,omitempty"`
	N             *int     `json:"n,omitempty"`
}

func readSpec(path string) (*videoSpec, string, *failure) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fail("spec_unreadable", "Cannot open the file supplied with --spec.")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	if err != nil || len(data) > maxJSONBytes {
		return nil, "", fail("invalid_spec", "Cannot read the spec within the 4 MiB limit.")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var s videoSpec
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF {
		return nil, "", fail("invalid_spec", "Use one JSON spec object with documented field names and types.")
	}
	if strings.TrimSpace(s.Model) == "" || strings.TrimSpace(s.Mode) == "" || strings.TrimSpace(s.Prompt) == "" || strings.TrimSpace(s.Ratio) == "" || s.Duration <= 0 {
		return nil, "", fail("invalid_spec", "The spec requires model, mode, prompt, ratio and a positive duration.")
	}
	if len(strings.TrimSpace(s.Prompt)) > 64<<10 {
		return nil, "", fail("prompt_too_long", "The prompt exceeds 64 KiB.")
	}
	if s.N != nil && *s.N != 1 {
		return nil, "", fail("invalid_video_count", "Only n=1 is supported.")
	}
	canonical, _ := json.Marshal(s)
	return &s, digest(canonical), nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type capability struct {
	ID                string   `json:"id"`
	Provider          string   `json:"provider,omitempty"`
	DisplayName       string   `json:"display_name,omitempty"`
	OutputTier        string   `json:"output_tier,omitempty"`
	Modes             []string `json:"modes,omitempty"`
	Ratios            []string `json:"ratios,omitempty"`
	Resolutions       []string `json:"resolutions,omitempty"`
	DefaultResolution string   `json:"default_resolution,omitempty"`
	DurationKind      string   `json:"duration_kind,omitempty"`
	FixedDuration     int      `json:"fixed_duration,omitempty"`
	MinDuration       int      `json:"min_duration,omitempty"`
	MaxDuration       int      `json:"max_duration,omitempty"`
	MinImages         int      `json:"min_images"`
	MaxImages         *int     `json:"max_images,omitempty"`
	VideoRequired     bool     `json:"video_required"`
	VideoSupported    *bool    `json:"video_supported,omitempty"`
	MinVideos         int      `json:"min_videos,omitempty"`
	MaxVideos         int      `json:"max_videos,omitempty"`
	VideoModes        []string `json:"video_modes,omitempty"`
	AudioSupported    *bool    `json:"audio_supported,omitempty"`
	MinAudios         int      `json:"min_audios,omitempty"`
	MaxAudios         int      `json:"max_audios,omitempty"`
	AudioModes        []string `json:"audio_modes,omitempty"`
	RemoteMedia       *bool    `json:"remote_media,omitempty"`
}

func (s *videoSpec) validate(models []capability) *failure {
	i := slices.IndexFunc(models, func(m capability) bool { return m.ID == s.Model })
	if i < 0 {
		return fail("unsupported_model", "Choose a model from ifytv models.")
	}
	m := models[i]
	if !slices.Contains([]string{"omni", "text_to_video", "image_to_video", "first_last_frame"}, s.Mode) || (len(m.Modes) > 0 && !slices.Contains(m.Modes, s.Mode)) {
		return fail("unsupported_mode", "The model does not support the requested mode.")
	}
	if len(m.Ratios) > 0 && !slices.ContainsFunc(m.Ratios, func(v string) bool { return strings.EqualFold(v, strings.TrimSpace(s.Ratio)) }) {
		return fail("invalid_ratio", "Choose an aspect ratio advertised by this model.")
	}
	switch m.DurationKind {
	case "fixed":
		if s.Duration != m.FixedDuration {
			return fail("invalid_duration", fmt.Sprintf("This model requires a duration of %d seconds.", m.FixedDuration))
		}
	case "range":
		if s.Duration < m.MinDuration || s.Duration > m.MaxDuration {
			return fail("invalid_duration", fmt.Sprintf("Use a duration between %d and %d seconds.", m.MinDuration, m.MaxDuration))
		}
	case "", "unlimited":
	default:
		return fail("invalid_catalog", "The model has an unknown duration policy.")
	}
	if s.Resolution != "" {
		normalize := func(v string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), "p") }
		if !slices.ContainsFunc(m.Resolutions, func(v string) bool { return normalize(v) == normalize(s.Resolution) }) {
			return fail("invalid_resolution", "The model does not support the requested resolution.")
		}
	}
	n := len(s.Images)
	if n < m.MinImages || (m.MaxImages != nil && n > *m.MaxImages) || (s.Mode == "text_to_video" && n != 0) ||
		(s.Mode == "image_to_video" && n < 1) || (s.Mode == "first_last_frame" && (n < 1 || n > 2)) {
		return fail("invalid_image_count", "The image count does not match the selected model and mode.")
	}
	if m.RemoteMedia != nil && !*m.RemoteMedia && (n > 0 || len(s.Audio) > 0 || len(s.Videos) > 0) {
		return fail("remote_media_not_supported", "This model cannot use uploaded reference URLs. Choose a model with remote_media support.")
	}
	if m.VideoRequired && s.VideoURL == "" {
		return fail("video_url_required", "This model requires a video_url reference.")
	}
	videoCount := len(s.Videos)
	if s.VideoURL != "" {
		videoCount++
	}
	videoAllowed := m.VideoRequired || (permitted(m.VideoSupported) && modeAllowed(m.VideoModes, s.Mode))
	if !countAllowed(videoCount, videoAllowed, m.MinVideos, m.MaxVideos) {
		return fail("invalid_video_count", "The video references do not match this model's supported modes or counts.")
	}
	audioAllowed := permitted(m.AudioSupported) && modeAllowed(m.AudioModes, s.Mode)
	if !countAllowed(len(s.Audio), audioAllowed, m.MinAudios, m.MaxAudios) {
		return fail("invalid_audio_count", "The audio references do not match this model's supported modes or counts.")
	}
	return nil
}

func permitted(v *bool) bool { return v == nil || *v }
func modeAllowed(modes []string, mode string) bool {
	return len(modes) == 0 || slices.Contains(modes, mode)
}
func countAllowed(n int, allowed bool, minCount, maxCount int) bool {
	if !allowed {
		return n == 0
	}
	return n >= minCount && (maxCount <= 0 || n <= maxCount)
}

type reference struct {
	value                         *string
	kind, label, path, mime, hash string
	size                          int64
}

func (s *videoSpec) references() []reference {
	var refs []reference
	add := func(v *string, kind, label string) {
		refs = append(refs, reference{value: v, kind: kind, label: label})
	}
	for i := range s.Images {
		add(&s.Images[i], "image", fmt.Sprintf("image %d", i+1))
	}
	if s.VideoURL != "" {
		add(&s.VideoURL, "video", "video_url")
	}
	for i := range s.Videos {
		add(&s.Videos[i], "video", fmt.Sprintf("video %d", i+1))
	}
	for i := range s.Audio {
		add(&s.Audio[i], "audio", fmt.Sprintf("audio %d", i+1))
	}
	return refs
}

var extensionMIME = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".mp3": "audio/mpeg", ".m4a": "audio/mp4",
	".aac": "audio/aac", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".wav": "audio/wav",
}

func validMediaURL(s string) bool {
	if len(s) > 16<<10 || strings.Contains(s, "\\") || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && webURL(u) && u.Fragment == ""
}

func (r *reference) inspect(dir string, allowMissing bool) *failure {
	v := *r.value
	u, err := url.Parse(v)
	if !filepath.IsAbs(v) && err == nil && u.Scheme != "" {
		if !validMediaURL(v) {
			return fail("invalid_reference", r.label+" must be a local file or an HTTP(S) URL without embedded credentials.")
		}
		r.hash = digest([]byte(v))
		return nil
	}
	if v == "" || strings.HasPrefix(v, "//") || strings.HasPrefix(v, `\\`) || strings.Contains(v, "://") {
		return fail("invalid_reference", r.label+" must identify a local file or an HTTP(S) URL.")
	}
	r.path = v
	if !filepath.IsAbs(v) {
		r.path = filepath.Join(dir, v)
	}
	ext := strings.ToLower(filepath.Ext(r.path))
	r.mime = extensionMIME[ext]
	if ext == ".webm" && (r.kind == "audio" || r.kind == "video") {
		r.mime = r.kind + "/webm"
	}
	if !strings.HasPrefix(r.mime, r.kind+"/") {
		return fail("invalid_reference", r.label+" has an unsupported file type for this field.")
	}
	info, err := os.Stat(r.path)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return fail("invalid_reference", r.label+" must be a readable, nonempty regular file.")
	}
	f, err := os.Open(r.path)
	if err != nil {
		return fail("unreadable_reference", "Cannot open "+r.label+".")
	}
	defer f.Close()
	h := sha256.New()
	reader := io.TeeReader(f, h)
	var prefix [512]byte
	n, err := io.ReadFull(reader, prefix[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return fail("unreadable_reference", "Cannot read "+r.label+".")
	}
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(prefix[:n]))
	if (r.kind == "image" && sniffed != r.mime) || sniffed == "text/html" || sniffed == "text/xml" || sniffed == "application/xml" {
		return fail("invalid_reference", r.label+" has content inconsistent with its media type.")
	}
	remaining, err := io.Copy(io.Discard, reader)
	r.size = int64(n) + remaining
	if err != nil || r.size != info.Size() {
		return fail("unreadable_reference", r.label+" changed or could not be read completely.")
	}
	r.hash = hex.EncodeToString(h.Sum(nil))
	return nil
}
