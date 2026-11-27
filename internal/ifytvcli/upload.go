package ifytvcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"time"
)

// Only call this for a persisted upload in the pre-create stage. Signed GETs
// need not authorize HEAD; a one-byte GET also works when a CDN ignores Range.
func (c *client) checkUpload(ctx context.Context, rawURL, label string) (bool, *failure) {
	const probeTimeout = 15 * time.Second
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// Receipts can point to a CDN. Never attach site credentials to the probe.
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, corruptState()
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.http.Do(req)
	status := 0
	if err == nil {
		defer resp.Body.Close()
		status = resp.StatusCode
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
			return false, nil // The saved URL no longer grants read access.
		case http.StatusOK, http.StatusPartialContent:
			contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			switch contentType {
			case "text/html", "application/json", "application/xml", "text/xml":
				// A CDN or login error page does not establish availability.
			default:
				var first [1]byte
				if _, err := io.ReadFull(resp.Body, first[:]); err == nil {
					return true, nil
				}
			}
		}
	}
	if ctx.Err() != nil {
		problem := contextFailure(ctx)
		problem.Message = label + ": " + problem.Message
		return false, problem
	}
	// A timeout, partial response or temporary storage error proves neither
	// presence nor expiry. Keep the receipt and stop before preparing a task.
	return false, &failure{Code: "upload_check_unavailable", HTTPStatus: status,
		Message: "Cannot verify the saved upload for " + label + ". Retry the same run when the media host is available."}
}

func (c *client) uploadWithRetry(ctx context.Context, ref reference) (string, *failure) {
	return c.uploadToWithRetry(ctx, "/api/v1/uploads", ref)
}

func (c *client) uploadToWithRetry(ctx context.Context, endpoint string, ref reference) (string, *failure) {
	const maxAttempts = 4
	const maxDelay = 30 * time.Second
	delay := 250 * time.Millisecond
	for attempt := 1; ; attempt++ {
		url, problem := c.upload(ctx, endpoint, ref)
		if problem == nil || problem.HTTPStatus != 429 || attempt == maxAttempts {
			return url, problem
		}
		// A worker keeps its slot while waiting. Jitter prevents several CLI
		// instances from necessarily retrying at the same instant.
		wait := max(delay+rand.N(delay/2), problem.retryAfter)
		if wait > maxDelay {
			problem.Message = ref.label + ": the site asked for a longer delay. Retry the same run after " + problem.retryAfter.Round(time.Second).String() + "."
			return "", problem
		}
		if problem := pause(ctx, wait); problem != nil {
			problem.Message = ref.label + ": " + problem.Message
			return "", problem
		}
		delay *= 2
	}
}

// Build just the multipart framing in memory. The file is streamed with a
// known Content-Length, and must still match its preflight digest.
func multipartFrame(ref reference) (head, tail []byte, contentType string, err error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	headers := textproto.MIMEHeader{}
	headers.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filepath.Base(ref.path)}))
	headers.Set("Content-Type", ref.mime)
	if _, err = w.CreatePart(headers); err != nil {
		return
	}
	split := b.Len()
	if err = w.Close(); err != nil {
		return
	}
	return b.Bytes()[:split], b.Bytes()[split:], w.FormDataContentType(), nil
}

func (c *client) upload(ctx context.Context, endpoint string, ref reference) (string, *failure) {
	file, err := os.Open(ref.path)
	if err != nil {
		return "", fail("unreadable_reference", "Cannot open "+ref.label+" for upload.")
	}
	defer file.Close()
	head, tail, mimeType, err := multipartFrame(ref)
	if err != nil {
		return "", fail("invalid_reference", "Cannot encode "+ref.label+" for upload.")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	req, problem := c.request(ctx, "POST", endpoint, reader, true)
	if problem != nil {
		writer.Close()
		return "", problem
	}
	req.ContentLength = int64(len(head)) + ref.size + int64(len(tail))
	req.Header.Set("Content-Type", mimeType)
	sent := make(chan bool, 1)
	go func() {
		h := sha256.New()
		_, err := writer.Write(head)
		var n int64
		if err == nil {
			n, err = io.CopyN(io.MultiWriter(writer, h), file, ref.size)
		}
		if err == nil && (n != ref.size || hex.EncodeToString(h.Sum(nil)) != ref.hash) {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			// Detect an append after preflight as well as truncation or replacement.
			var extra [1]byte
			if n, last := file.Read(extra[:]); n != 0 || last != io.EOF {
				err = io.ErrUnexpectedEOF
			}
		}
		if err == nil {
			_, err = writer.Write(tail)
		}
		writer.CloseWithError(err)
		sent <- err == nil
	}()
	resp, problem := c.send(req)
	// An early HTTP error can arrive before the transport consumes the file.
	// Wake the writer and join it before closing the underlying descriptor.
	reader.Close()
	complete := <-sent
	if problem != nil {
		problem.Message = ref.label + ": " + problem.Message
		return "", problem
	}
	defer resp.Body.Close()
	if !complete {
		return "", fail("upload_incomplete", ref.label+" was not completely uploaded. Check the reference and retry.")
	}
	var reply struct {
		URL  string `json:"url"`
		Kind string `json:"media_type"`
		Size *int64 `json:"size"`
	}
	if problem := readJSON(resp.Body, &reply); problem != nil {
		problem.Message = ref.label + ": " + problem.Message
		return "", problem
	}
	if !validMediaURL(reply.URL) || (reply.Kind != "" && reply.Kind != ref.kind) || (reply.Size != nil && *reply.Size != ref.size) {
		return "", fail("invalid_upload_response", ref.label+" did not receive a matching upload receipt with a valid media URL.")
	}
	return reply.URL, nil
}
