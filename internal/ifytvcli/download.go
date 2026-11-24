package ifytvcli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

const maxDownloadBytes = 2 << 30

func (c *client) download(ctx context.Context, result output, destination string) (output, *failure) {
	path, err := filepath.Abs(destination)
	if err != nil {
		return result, fail("invalid_output", "Cannot resolve the output file path.")
	}
	if _, err = os.Lstat(path); err == nil {
		return result, outputExists()
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fail("invalid_output", "Cannot inspect the output file path.")
	}
	req, problem := c.request(ctx, "GET", "/api/v1/tasks/"+result.TaskID+"/download", nil, true)
	if problem != nil {
		return result, problem
	}
	req.Header.Set("Accept", "video/mp4, application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, problem := c.send(req)
	if problem != nil {
		return result, problem
	}
	defer resp.Body.Close()
	size := resp.ContentLength
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Range") != "" || size < 1 || size > maxDownloadBytes {
		return result, fail("invalid_download", "The site did not provide a complete video with a valid Content-Length.")
	}
	typ, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch typ {
	case "video/mp4", "application/mp4", "application/octet-stream", "binary/octet-stream", "":
	default:
		return result, fail("invalid_download", "The download response is not an MP4 video.")
	}
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(8)
	if err != nil || !mp4StartBox(string(prefix[4:8])) {
		return result, fail("invalid_download", "The response is incomplete or missing its MP4 file header.")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ifytv-download-*")
	if err != nil {
		return result, fail("output_unwritable", "Cannot create a temporary file in the output directory.")
	}
	defer os.Remove(temp.Name())
	n, err := io.Copy(temp, io.LimitReader(reader, size+1))
	valid := err == nil && n == size && validMP4Video(ctx, temp, n)
	if valid {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if ctx.Err() != nil {
		return result, contextFailure(ctx)
	}
	if err != nil || n != size || closeErr != nil {
		return result, fail("download_incomplete", "Could not save the complete video. Check the connection and available disk space.")
	}
	if !valid {
		return result, fail("invalid_download", "The downloaded MP4 is incomplete or has no valid video samples. Retry the download.")
	}
	if err := fileops.Publish(temp.Name(), path, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			return result, outputExists()
		}
		return result, fail("output_unwritable", "Cannot publish the completed file in the output directory.")
	}
	result.Path, result.Bytes = path, n
	return result, nil
}

func outputExists() *failure {
	return fail("output_exists", "The output file already exists. Choose another --out path.")
}
