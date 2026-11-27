package ifytvcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

const canvasAssetChecksumHeader = "X-IFYTV-Asset-SHA256"

var canvasAssetMIME = map[string][]string{
	".png":  {"image/png"},
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
	".webp": {"image/webp"},
	".gif":  {"image/gif"},
	".mp3":  {"audio/mpeg"},
	".wav":  {"audio/wav", "audio/wave", "audio/x-wav", "audio/vnd.wave"},
	".m4a":  {"audio/mp4", "audio/x-m4a"},
	".aac":  {"audio/aac"},
	".ogg":  {"audio/ogg"},
	".oga":  {"audio/ogg"},
	".mp4":  {"video/mp4"},
	".webm": {"video/webm", "audio/webm"},
	".mov":  {"video/quicktime"},
	".txt":  {"text/plain", "text/markdown"},
	".md":   {"text/plain", "text/markdown"},
}

func (c *client) fetchCanvasAsset(ctx context.Context, o options) (output, *failure) {
	var result output
	if !validUUID(o.project) || !validUUID(o.asset) {
		return result, fail("usage", "Fetch requires project and asset UUIDs.")
	}
	result.AssetID = strings.ToLower(o.asset)
	c.addCanvasLocation(&result, strings.ToLower(o.project), "")
	if problem := c.requireProductionDevice(o); problem != nil {
		return result, problem
	}

	path, types, problem := canvasAssetOutput(o.out)
	if problem != nil {
		return result, problem
	}
	temp, err := createStateTemp(filepath.Dir(path))
	if err != nil {
		return result, fail("output_unwritable", "Cannot create a private temporary file in the output directory.")
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	c = c.productionClient()
	endpoint := "/api/v1/cli/projects/" + result.ProjectID + "/assets/" + result.AssetID + "/download"
	req, problem := c.request(ctx, http.MethodGet, endpoint, nil, true)
	if problem != nil {
		return result, problem
	}
	req.Header.Set("Accept", strings.Join(types, ", "))
	req.Header.Set("Accept-Encoding", "identity")
	checkRedirect := c.http.CheckRedirect
	c.http.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		// A redirect may not exchange the requested project or asset for another resource.
		if next.URL.RequestURI() != req.URL.RequestURI() || next.URL.Fragment != "" {
			return errRedirect
		}
		return checkRedirect(next, via)
	}
	resp, problem := c.send(req)
	if problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	defer resp.Body.Close()
	if origin(resp.Request.URL) != origin(req.URL) || resp.Request.URL.RequestURI() != req.URL.RequestURI() ||
		resp.Request.Header.Get("Authorization") != req.Header.Get("Authorization") {
		return result, fail("redirect_rejected", "The response did not come from the authorized project asset route.")
	}
	n, problem := saveCanvasAsset(ctx, resp, path, temp, types)
	if problem == nil {
		result.Path, result.Bytes = path, n
	}
	return result, problem
}

func canvasAssetOutput(destination string) (string, []string, *failure) {
	invalid := fail("invalid_output", "Choose a local --out path with a supported asset extension.")
	types := canvasAssetMIME[strings.ToLower(filepath.Ext(destination))]
	volume := filepath.VolumeName(destination)
	if len(types) == 0 || strings.IndexFunc(destination, unicode.IsControl) != -1 ||
		strings.HasPrefix(strings.ReplaceAll(destination, "\\", "/"), "//") ||
		(volume != "" && (len(volume) != 2 || !filepath.IsAbs(destination))) ||
		strings.Contains(destination[len(volume):], ":") {
		return "", nil, invalid
	}
	path, err := filepath.Abs(destination)
	if err != nil {
		return "", nil, invalid
	}
	// Check the normalized path for an implicit UNC volume and reserved device names.
	volume = filepath.VolumeName(path)
	if strings.HasPrefix(filepath.ToSlash(path), "//") || (volume != "" && len(volume) != 2) ||
		!filepath.IsLocal(strings.TrimLeft(path[len(volume):], "/\\")) {
		return "", nil, invalid
	}
	if _, err := os.Lstat(path); err == nil {
		return "", nil, outputExists()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fail("invalid_output", "Cannot inspect the output file path.")
	}
	return path, types, nil
}

func saveCanvasAsset(ctx context.Context, resp *http.Response, path string, temp *os.File, types []string) (int64, *failure) {
	size := resp.ContentLength
	encodings := resp.Header.Values("Content-Encoding")
	if resp.StatusCode != http.StatusOK || len(resp.Header.Values("Content-Range")) != 0 ||
		len(resp.Header.Values("Content-Length")) != 1 || size < 1 || size > maxDownloadBytes ||
		resp.Uncompressed || len(encodings) > 1 || (len(encodings) == 1 && !strings.EqualFold(encodings[0], "identity")) {
		return 0, fail("invalid_download", "The site did not provide a complete asset with a valid Content-Length.")
	}
	contentTypes := resp.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return 0, fail("invalid_download", "The site did not identify the asset's format.")
	}
	typ, params, err := mime.ParseMediaType(contentTypes[0])
	charset, hasCharset := params["charset"]
	if err != nil || !slices.Contains(types, typ) ||
		(strings.HasPrefix(typ, "text/") && hasCharset && !strings.EqualFold(charset, "utf-8")) {
		return 0, fail("invalid_download", "The asset's format does not match the selected output extension.")
	}
	checksums := resp.Header.Values(canvasAssetChecksumHeader)
	if len(checksums) != 1 || !validHash(checksums[0]) {
		return 0, fail("invalid_download", "The site did not provide a valid asset SHA-256 checksum.")
	}

	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(resp.Body, size+1))
	if ctx.Err() != nil {
		return 0, contextFailure(ctx)
	}
	if err != nil || n != size {
		return 0, fail("download_incomplete", "Could not save the complete asset. Check the connection and available disk space.")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksums[0]) {
		return 0, fail("invalid_download", "The asset bytes did not match the site's SHA-256 checksum.")
	}
	if syncErr, closeErr := temp.Sync(), temp.Close(); syncErr != nil || closeErr != nil {
		return 0, fail("output_unwritable", "Cannot save the verified asset in the output directory.")
	}
	if ctx.Err() != nil {
		return 0, contextFailure(ctx)
	}
	if err := fileops.Publish(temp.Name(), path, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, outputExists()
		}
		return 0, fail("output_unwritable", "Cannot publish the verified asset in the output directory.")
	}
	return n, nil
}
