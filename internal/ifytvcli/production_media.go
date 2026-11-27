package ifytvcli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

const (
	productionCapabilitiesPath = "/api/v1/cli/media/capabilities"
	maxProductionSpecBytes     = 256 << 10
	maxProductionImageBytes    = 32 << 20
	maxProductionSpeechBytes   = 32 << 20
	maxProductionVideoBytes    = 512 << 20
)

type productionVoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type productionMediaCapabilities struct {
	ImageModels    []string          `json:"image_models"`
	Voices         []productionVoice `json:"voices"`
	PublishEpisode bool              `json:"publish_episode"`
}

func (c *client) requireProductionDevice(o options) *failure {
	if !validDeviceToken(c.key) || !validRunScope(c.runScope) || !validRunScope(o.authorizationID) {
		return fail("device_login_required", "This command requires the pinned browser approval. Sign in and pass its --authorization-id.")
	}
	return nil
}

// These endpoints return private project data and files. Keep the request chain on
// the site's origin, including redirects, and preserve the site's HTTP client.
func (c client) productionClient() *client {
	httpClient := *c.http
	checkRedirect := httpClient.CheckRedirect
	siteOrigin := origin(c.base)
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if origin(req.URL) != siteOrigin {
			return errRedirect
		}
		return checkRedirect(req, via)
	}
	c.http = &httpClient
	return &c
}

func (c *client) productionCapabilities(ctx context.Context, o options) (output, *failure) {
	var result output
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}
	caps, problem := c.productionClient().readProductionCapabilities(ctx)
	result.MediaCapabilities = caps
	return result, problem
}

func (c *client) readProductionCapabilities(ctx context.Context) (*productionMediaCapabilities, *failure) {
	var caps productionMediaCapabilities
	if problem := c.json(ctx, http.MethodGet, productionCapabilitiesPath, nil, "", true, &caps, http.StatusOK); problem != nil {
		if problem.HTTPStatus == http.StatusNotFound || problem.Code == "device_scope_denied" {
			problem.Code = "media_capability_unavailable"
			problem.Message = "The site has not enabled production media for this device approval."
		}
		return nil, problem
	}
	invalid := fail("invalid_response", "The site returned an invalid production media catalog.")
	if caps.ImageModels == nil || caps.Voices == nil || len(caps.ImageModels) > 512 || len(caps.Voices) > 512 {
		return nil, invalid
	}
	models := make(map[string]bool, len(caps.ImageModels))
	for _, id := range caps.ImageModels {
		if !validProductionID(id) || models[id] {
			return nil, invalid
		}
		models[id] = true
	}
	voices := make(map[string]bool, len(caps.Voices))
	for _, voice := range caps.Voices {
		if !validProductionID(voice.ID) || !validProductionText(voice.Name, 256) ||
			strings.IndexFunc(voice.Name, unicode.IsControl) >= 0 || voices[voice.ID] {
			return nil, invalid
		}
		voices[voice.ID] = true
	}
	return &caps, nil
}

func validProductionText(value string, bound int) bool {
	return len(value) <= bound && strings.TrimSpace(value) != "" &&
		strings.IndexFunc(value, func(r rune) bool {
			return unicode.IsControl(r) && r != '\r' && r != '\n' && r != '\t'
		}) == -1
}

func validProductionID(value string) bool {
	return len(value) > 0 && len(value) <= 256 && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) == -1
}

type productionMediaSpec struct {
	Kind       string   `json:"kind"`
	Model      string   `json:"model,omitempty"`
	Prompt     string   `json:"prompt,omitempty"`
	Ratio      string   `json:"ratio,omitempty"`
	References []string `json:"references,omitempty"`
	Voice      string   `json:"voice,omitempty"`
	Text       string   `json:"text,omitempty"`
	Emotion    string   `json:"emotion,omitempty"`
}

func readProductionSpec(path string) (productionMediaSpec, *failure) {
	var spec productionMediaSpec
	f, err := os.Open(path)
	if err != nil {
		return spec, fail("spec_unreadable", "Cannot open the media specification.")
	}
	defer f.Close()
	input, err := io.ReadAll(io.LimitReader(f, maxProductionSpecBytes+1))
	if err != nil || len(input) > maxProductionSpecBytes || !utf8.Valid(input) {
		return spec, fail("invalid_spec", "Use a UTF-8 media JSON file within 256 KiB.")
	}
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(input, []byte{0xef, 0xbb, 0xbf})))
	d.DisallowUnknownFields()
	if err := d.Decode(&spec); err != nil || d.Decode(new(json.RawMessage)) != io.EOF {
		return spec, fail("invalid_spec", "Use one media JSON object with documented fields and types.")
	}
	switch spec.Kind {
	case "image":
		if !validProductionID(spec.Model) || !validProductionText(spec.Prompt, 64<<10) ||
			!slices.Contains([]string{"9:16", "16:9", "1:1"}, spec.Ratio) || len(spec.References) > 8 ||
			spec.Voice != "" || spec.Text != "" || spec.Emotion != "" {
			return spec, fail("invalid_spec", "Image media requires a model, a prompt, a supported ratio, and at most eight local reference images.")
		}
	case "speech":
		if !validProductionID(spec.Voice) || !validProductionText(spec.Text, 16<<10) ||
			(spec.Emotion != "" && !validProductionText(spec.Emotion, 256)) ||
			spec.Model != "" || spec.Prompt != "" || spec.Ratio != "" || spec.References != nil {
			return spec, fail("invalid_spec", "Speech media requires a voice and text, with an optional short emotion instruction.")
		}
	default:
		return spec, fail("invalid_spec", "The media kind must be image or speech.")
	}
	return spec, nil
}

func productionLocalReference(ref *reference, dir string, maxBytes int64) *failure {
	value := *ref.value
	if value == "" || strings.Contains(value, "://") || strings.HasPrefix(filepath.ToSlash(value), "//") || strings.HasPrefix(value, `\\`) {
		return fail("invalid_reference", ref.label+" must be a local file.")
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, value)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxBytes {
		return fail("invalid_reference", ref.label+" must be a readable, nonempty local file within the size limit.")
	}
	if problem := ref.inspect(dir, false); problem != nil {
		return problem
	}
	if ref.path == "" || ref.size > maxBytes {
		return fail("invalid_reference", ref.label+" must be a local file within the size limit.")
	}
	return nil
}

func (s productionMediaSpec) supportedBy(caps *productionMediaCapabilities) bool {
	if s.Kind == "image" {
		return slices.Contains(caps.ImageModels, s.Model)
	}
	return slices.ContainsFunc(caps.Voices, func(voice productionVoice) bool { return voice.ID == s.Voice })
}

func productionMediaPath(project string) string {
	return "/api/v1/cli/projects/" + project + "/media"
}

func (c *client) stageProductionMedia(ctx context.Context, result *output, ref reference) (string, *failure) {
	endpoint := productionMediaPath(result.ProjectID) + "/uploads"
	raw, problem := c.uploadToWithRetry(ctx, endpoint, ref)
	if problem != nil {
		c.addProjectConsent(result, problem)
		return "", problem
	}
	if _, ok := c.productionUploadPath(result.ProjectID, raw); !ok {
		return "", fail("invalid_upload_response", "The site did not return a private staging URL for the requested project.")
	}
	return raw, nil
}

func (c *client) productionUploadPath(project, raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || !webURL(u) || origin(u) != origin(c.base) || u.RawPath != "" ||
		u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return "", false
	}
	endpoint := productionMediaPath(project) + "/uploads/"
	id, ok := strings.CutPrefix(u.Path, c.base.Path+endpoint)
	if !ok || !validUUID(id) {
		return "", false
	}
	return endpoint + id, true
}

// Upload URLs can expire, and a fresh browser approval can issue a different
// token for the same account. Keep either change out of the idempotency key.
func (c *client) productionKey(operation string, o options) string {
	return digest([]byte(strings.Join([]string{
		"ifytv_cli_production_v1", operation, c.base.String(), c.runScope, o.project, o.run,
	}, "\x00")))
}

type productionMediaTask struct {
	ID   string `json:"task_id"`
	Kind string `json:"kind"`
	Task
}

func (task *productionMediaTask) validate(kind, existingID string) *failure {
	task.ID = strings.ToLower(task.ID)
	if task.Kind != kind || !validUUID(task.ID) || (existingID != "" && task.ID != existingID) {
		return fail("invalid_response", "The site did not identify the expected production media task. Retry the same run.")
	}
	switch task.Status {
	case "queued", "running", "succeeded", "failed":
	default:
		return fail("invalid_response", "The site returned an unknown production media status. Retry the same run.")
	}
	task.Phase, task.ErrorCode = safeCode(task.Phase), safeCode(task.ErrorCode)
	if p := task.Progress; p != nil && (*p < 0 || *p > 100) {
		task.Progress = nil
	}
	return nil
}

func (c *client) generateProductionMedia(ctx context.Context, o options) (output, *failure) {
	result := output{Run: o.run}
	c.addCanvasLocation(&result, o.project, "")
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}
	c = c.productionClient()
	spec, problem := readProductionSpec(o.spec)
	if problem != nil {
		return result, problem
	}
	path, temp, problem := prepareProductionDownload(o.out, spec.Kind)
	if problem != nil {
		return result, problem
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	refs := make([]reference, len(spec.References))
	for i := range spec.References {
		refs[i] = reference{value: &spec.References[i], label: fmt.Sprintf("image %d", i+1), kind: "image"}
		if problem := productionLocalReference(&refs[i], filepath.Dir(o.spec), maxProductionImageBytes); problem != nil {
			return result, problem
		}
	}
	caps, problem := c.readProductionCapabilities(ctx)
	if problem != nil {
		return result, problem
	}
	if !spec.supportedBy(caps) {
		return result, fail("media_capability_unavailable", "The selected image model or voice is unavailable under this device approval.")
	}

	var hashes []string
	for _, ref := range refs {
		url, problem := c.stageProductionMedia(ctx, &result, ref)
		if problem != nil {
			return result, problem
		}
		*ref.value = url
		hashes = append(hashes, ref.hash)
	}
	payload, _ := json.Marshal(struct {
		productionMediaSpec
		ReferenceSHA256 []string `json:"reference_sha256,omitempty"`
	}{spec, hashes})
	endpoint := productionMediaPath(o.project) + "/tasks"
	var receipt productionMediaTask
	if problem := c.json(ctx, http.MethodPost, endpoint, payload, c.productionKey("generate", o), true, &receipt, http.StatusCreated, http.StatusOK); problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	if problem := receipt.validate(spec.Kind, ""); problem != nil {
		return result, problem
	}
	result.TaskID, result.Task = receipt.ID, receipt.Task
	return c.finishProductionMedia(ctx, result, spec.Kind, path, temp, o.interval)
}

func (c *client) fetchProductionMedia(ctx context.Context, o options) (output, *failure) {
	result := output{TaskID: o.task}
	c.addCanvasLocation(&result, o.project, "")
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}
	if !validUUID(o.task) || (o.kind != "image" && o.kind != "speech") {
		return result, fail("usage", "Fetch requires a task UUID and kind image or speech.")
	}
	c = c.productionClient()
	path, temp, problem := prepareProductionDownload(o.out, o.kind)
	if problem != nil {
		return result, problem
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	taskPath := productionMediaPath(o.project) + "/tasks/" + o.task
	var receipt productionMediaTask
	if problem := c.json(ctx, http.MethodGet, taskPath, nil, "", true, &receipt, http.StatusOK); problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	if problem := receipt.validate(o.kind, o.task); problem != nil {
		return result, problem
	}
	result.Task = receipt.Task
	return c.finishProductionMedia(ctx, result, o.kind, path, temp, o.interval)
}

func (c *client) finishProductionMedia(ctx context.Context, result output, kind, path string, temp *os.File, interval time.Duration) (output, *failure) {
	taskPath := productionMediaPath(result.ProjectID) + "/tasks/" + result.TaskID
	if problem := c.waitProductionMedia(ctx, &result, taskPath, kind, interval); problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	problem := c.downloadProductionMedia(ctx, &result, taskPath+"/download", kind, path, temp)
	if problem != nil {
		c.addProjectConsent(&result, problem)
	}
	return result, problem
}

func (c *client) waitProductionMedia(ctx context.Context, result *output, path, kind string, interval time.Duration) *failure {
	delay, last := interval, ""
	for {
		switch result.Status {
		case "succeeded":
			return nil
		case "failed":
			return fail("media_task_failed", "Production of this asset failed. Check the task status.")
		}
		message := result.Status
		if result.Progress != nil {
			message += fmt.Sprintf(" %d%%", *result.Progress)
		}
		if message != last {
			c.report(message)
			last = message
		}
		if problem := pause(ctx, delay); problem != nil {
			return problem
		}
		var next productionMediaTask
		problem := c.json(ctx, http.MethodGet, path, nil, "", true, &next, http.StatusOK)
		if problem != nil {
			if !problem.retryable() {
				return problem
			}
			delay = max(interval, problem.retryAfter)
			continue
		}
		if problem := next.validate(kind, result.TaskID); problem != nil {
			return problem
		}
		result.Task = next.Task
		delay = interval
	}
}

func prepareProductionDownload(destination, kind string) (string, *os.File, *failure) {
	extension := ".png"
	if kind == "speech" {
		extension = ".mp3"
	}
	path, err := filepath.Abs(destination)
	if err != nil || !strings.EqualFold(filepath.Ext(path), extension) {
		return "", nil, fail("invalid_output", "Choose a local "+extension+" output path for the requested asset.")
	}
	if _, err = os.Lstat(path); err == nil {
		return "", nil, outputExists()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fail("invalid_output", "Cannot inspect the output file path.")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ifytv-production-*")
	if err != nil {
		return "", nil, fail("output_unwritable", "Cannot create a temporary file in the output directory.")
	}
	return path, temp, nil
}

func (c *client) downloadProductionMedia(ctx context.Context, result *output, endpoint, kind, path string, temp *os.File) *failure {
	wantType, maxBytes := "image/png", int64(maxProductionImageBytes)
	if kind == "speech" {
		wantType, maxBytes = "audio/mpeg", maxProductionSpeechBytes
	}
	req, problem := c.request(ctx, http.MethodGet, endpoint, nil, true)
	if problem != nil {
		return problem
	}
	req.Header.Set("Accept", wantType+", application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, problem := c.send(req)
	if problem != nil {
		return problem
	}
	defer resp.Body.Close()
	if origin(resp.Request.URL) != origin(c.base) || resp.Request.Header.Get("Authorization") != req.Header.Get("Authorization") {
		return fail("redirect_rejected", "The site redirected a private media download to a different origin.")
	}

	contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	size := resp.ContentLength
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" || size < 1 || size > maxBytes ||
		(contentType != wantType && contentType != "application/octet-stream") {
		return fail("invalid_download", "The site did not return a complete asset in the expected format.")
	}
	n, err := io.Copy(temp, io.LimitReader(resp.Body, size+1))
	if ctx.Err() != nil {
		return contextFailure(ctx)
	}
	if err != nil || n != size {
		return fail("download_incomplete", "Could not save the complete asset. Check the connection and available disk space.")
	}
	valid := false
	if kind == "image" {
		valid = validProductionPNG(temp)
	} else {
		valid = validProductionMP3(ctx, temp, n)
	}
	if ctx.Err() != nil {
		return contextFailure(ctx)
	}
	if !valid {
		return fail("invalid_download", "The downloaded asset is damaged or does not match the expected media format.")
	}
	if syncErr, closeErr := temp.Sync(), temp.Close(); syncErr != nil || closeErr != nil {
		return fail("output_unwritable", "Cannot save the validated asset.")
	}
	if err := fileops.Publish(temp.Name(), path, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			return outputExists()
		}
		return fail("output_unwritable", "Cannot publish the validated asset in the output directory.")
	}
	result.Path, result.Bytes = path, n
	return nil
}

func validProductionPNG(f *os.File) bool {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	config, err := png.DecodeConfig(f)
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 ||
		int64(config.Width)*int64(config.Height) > 16<<20 {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	_, err = png.Decode(f)
	return err == nil
}

// Scan complete MPEG Layer III frames. This rejects a metadata-only file or a
// truncated stream even if the server provided a matching HTTP Content-Length.
func validProductionMP3(ctx context.Context, r io.ReaderAt, size int64) bool {
	var tag [10]byte
	if _, err := r.ReadAt(tag[:], 0); err != nil {
		return false
	}
	var offset int64
	if string(tag[:3]) == "ID3" {
		if tag[3] < 2 || tag[3] > 4 {
			return false
		}
		for _, part := range tag[6:] {
			if part&0x80 != 0 {
				return false
			}
			offset = offset<<7 | int64(part)
		}
		offset += 10
		if tag[3] == 4 && tag[5]&0x10 != 0 {
			offset += 10
		}
	}
	var header [4]byte
	frames := 0
	for offset < size && ctx.Err() == nil {
		if _, err := r.ReadAt(header[:], offset); err != nil {
			return false
		}
		if size-offset == 128 && string(header[:3]) == "TAG" {
			return frames > 0
		}
		length := productionMP3FrameSize(binary.BigEndian.Uint32(header[:]))
		if length == 0 || length > size-offset {
			return false
		}
		offset += length
		frames++
	}
	return frames > 0 && offset == size && ctx.Err() == nil
}

func productionMP3FrameSize(header uint32) int64 {
	version, layer := (header>>19)&3, (header>>17)&3
	bitrateIndex, sampleIndex := (header>>12)&15, (header>>10)&3
	if header&0xffe00000 != 0xffe00000 || version == 1 || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || sampleIndex == 3 {
		return 0
	}
	sampleRate := [...]int64{44100, 48000, 32000}[sampleIndex]
	var factor, bitrate int64
	if version == 3 {
		factor = 144000
		bitrate = [...]int64{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}[bitrateIndex]
	} else {
		factor = 72000
		bitrate = [...]int64{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}[bitrateIndex]
		sampleRate /= 2
		if version == 0 {
			sampleRate /= 2
		}
	}
	return factor*bitrate/sampleRate + int64((header>>9)&1)
}

func (c *client) publishEpisode(ctx context.Context, o options) (output, *failure) {
	result := output{Run: o.run}
	c.addCanvasLocation(&result, o.project, "")
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}
	c = c.productionClient()
	source := reference{value: &o.file, label: "episode MP4", kind: "video"}
	if !strings.EqualFold(filepath.Ext(o.file), ".mp4") {
		return result, fail("invalid_reference", "Choose the completed episode MP4.")
	}
	if problem := productionLocalReference(&source, ".", maxProductionVideoBytes); problem != nil {
		return result, problem
	}
	f, err := os.Open(source.path)
	if err != nil {
		return result, fail("unreadable_reference", "Cannot open the completed episode MP4.")
	}
	valid := validMP4Video(ctx, f, source.size)
	_ = f.Close()
	if ctx.Err() != nil {
		return result, contextFailure(ctx)
	}
	if !valid {
		return result, fail("invalid_reference", "The episode file is incomplete or has no valid MP4 video samples.")
	}
	caps, problem := c.readProductionCapabilities(ctx)
	if problem != nil {
		return result, problem
	}
	if !caps.PublishEpisode {
		return result, fail("media_capability_unavailable", "The site has not enabled episode publishing under this device approval.")
	}
	url, problem := c.stageProductionMedia(ctx, &result, source)
	if problem != nil {
		return result, problem
	}
	payload, _ := json.Marshal(struct {
		Title       string `json:"title"`
		VideoURL    string `json:"video_url"`
		VideoSHA256 string `json:"video_sha256"`
	}{o.run, url, source.hash})
	var reply struct {
		ProjectID string `json:"project_id"`
		NodeID    string `json:"node_id"`
		AssetID   string `json:"asset_id"`
		Duplicate *bool  `json:"duplicate,omitempty"`
	}
	if problem := c.json(ctx, http.MethodPost, productionMediaPath(o.project)+"/episodes", payload, c.productionKey("publish", o), true, &reply, http.StatusCreated, http.StatusOK); problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	if !strings.EqualFold(reply.ProjectID, o.project) || !nodePattern.MatchString(reply.NodeID) || !validUUID(reply.AssetID) {
		return result, fail("invalid_response", "The site did not confirm a finished episode card in the requested project. Retry the same run.")
	}
	c.addCanvasLocation(&result, o.project, reply.NodeID)
	result.AssetID, result.Duplicate = strings.ToLower(reply.AssetID), reply.Duplicate
	result.Status = "succeeded"
	return result, nil
}
