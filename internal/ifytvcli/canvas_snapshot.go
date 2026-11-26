package ifytvcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Ruicod/ifytv-cli/internal/fileops"
)

const (
	maxCanvasSnapshotPages  = 32
	maxCanvasSnapshotItems  = 20_000
	maxCanvasSnapshotBytes  = 16 << 20
	maxCanvasSnapshotCursor = 4096
)

type canvasSnapshot struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Revision  uint64 `json:"revision"`
	CanEdit   bool   `json:"can_edit"`
	canvasSnapshotGraph
}

type canvasSnapshotPage struct {
	ProjectID  string  `json:"project_id"`
	Title      *string `json:"title"`
	Revision   uint64  `json:"revision"`
	CanEdit    *bool   `json:"can_edit"`
	NextCursor string  `json:"next_cursor"`
	canvasSnapshotGraph
}

type canvasSnapshotGraph struct {
	Groups      []canvasSnapshotGroup      `json:"groups"`
	Nodes       []canvasSnapshotNode       `json:"nodes"`
	Connections []canvasSnapshotConnection `json:"connections"`
}

type canvasSnapshotGroup struct {
	ID       string  `json:"id"`
	Title    *string `json:"title"`
	ParentID string  `json:"parent_id,omitempty"`
}

type canvasSnapshotNode struct {
	ID            string                       `json:"id"`
	Kind          string                       `json:"kind"`
	Title         string                       `json:"title,omitempty"`
	Text          string                       `json:"text,omitempty"`
	Prompt        string                       `json:"prompt,omitempty"`
	GroupID       string                       `json:"group_id,omitempty"`
	Selection     string                       `json:"selection,omitempty"`
	Assets        []canvasSnapshotAsset        `json:"assets,omitempty"`
	Generation    *canvasSnapshotGeneration    `json:"generation,omitempty"`
	VideoSettings *canvasSnapshotVideoSettings `json:"video_settings,omitempty"`
}

type canvasSnapshotAsset struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	MIMEType  string `json:"mime_type,omitempty"`
	Selection string `json:"selection,omitempty"`
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
}

type canvasSnapshotVideoSettings struct {
	Model         string `json:"model,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Duration      int    `json:"duration,omitempty"`
	Ratio         string `json:"ratio,omitempty"`
	Resolution    string `json:"resolution,omitempty"`
	FaceCheck     *bool  `json:"face_check,omitempty"`
	GenerateAudio *bool  `json:"generate_audio,omitempty"`
}

func (settings canvasSnapshotVideoSettings) valid() bool {
	if settings.Duration < 0 {
		return false
	}
	for _, value := range []string{settings.Model, settings.Mode, settings.Ratio, settings.Resolution} {
		if value != "" && !validProductionID(value) {
			return false
		}
	}
	return true
}

type canvasSnapshotGeneration struct {
	TaskID string `json:"task_id,omitempty"`
	Task
}

type canvasSnapshotConnection struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	TargetNodeID string `json:"target_node_id"`
	SourcePort   string `json:"source_port,omitempty"`
	TargetPort   string `json:"target_port,omitempty"`
}

func (c *client) inspectCanvas(ctx context.Context, o options) (output, *failure) {
	var result output
	if !validUUID(o.project) {
		return result, fail("invalid_project", "Supply a project UUID or a cloud canvas URL from this site.")
	}
	c.addCanvasLocation(&result, strings.ToLower(o.project), "")
	if problem := c.requireProductionDevice(o); problem != nil {
		problem.Message = "Canvas inspection requires pinned browser approval. Sign in with ifytv auth login and pass its --authorization-id."
		return result, problem
	}
	var destination string
	if o.out != "" {
		path, err := filepath.Abs(o.out)
		if err != nil {
			return result, fail("invalid_output", "Cannot resolve the snapshot output path.")
		}
		if _, err := os.Lstat(path); err == nil {
			return result, outputExists()
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, fail("invalid_output", "Cannot inspect the snapshot output path.")
		}
		destination = path
	}

	c = c.productionClient()
	snapshot, problem := c.readCanvasSnapshot(ctx, result.ProjectID)
	if problem != nil {
		c.addProjectConsent(&result, problem)
		return result, problem
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return result, invalidCanvasSnapshot()
	}
	if len(data) > maxCanvasSnapshotBytes {
		return result, canvasSnapshotTooLarge()
	}
	if c.snapshotContainsCredential(data) {
		return result, fail("invalid_response", "The site unexpectedly included a sign-in credential in the canvas data.")
	}
	if ctx.Err() != nil {
		return result, contextFailure(ctx)
	}
	if destination == "" {
		result.Canvas = snapshot
		return result, nil
	}
	n, problem := writeCanvasSnapshot(ctx, destination, data)
	if problem != nil {
		return result, problem
	}
	result.Path, result.Bytes = destination, n
	return result, nil
}

func (c *client) readCanvasSnapshot(ctx context.Context, project string) (*canvasSnapshot, *failure) {
	var snapshot *canvasSnapshot
	var items, inputBytes int
	basePath := "/api/v1/cli/projects/" + project + "/snapshot"
	path := basePath
	seenCursors := make(map[string]bool)
	for range maxCanvasSnapshotPages {
		var raw json.RawMessage
		if problem := c.json(ctx, http.MethodGet, path, nil, "", true, &raw, http.StatusOK); problem != nil {
			return nil, canvasSnapshotFailure(problem)
		}
		inputBytes += len(raw)
		if inputBytes > maxCanvasSnapshotBytes {
			return nil, canvasSnapshotTooLarge()
		}
		var page canvasSnapshotPage
		if !utf8.Valid(raw) || json.Unmarshal(raw, &page) != nil || page.ProjectID != project ||
			page.Title == nil || page.Revision == 0 || page.CanEdit == nil ||
			page.Groups == nil || page.Nodes == nil || page.Connections == nil {
			return nil, invalidCanvasSnapshot()
		}
		items += len(page.Groups) + len(page.Nodes) + len(page.Connections)
		for _, node := range page.Nodes {
			items += len(node.Assets)
		}
		if items > maxCanvasSnapshotItems {
			return nil, canvasSnapshotTooLarge()
		}
		if snapshot == nil {
			snapshot = &canvasSnapshot{ProjectID: project, Title: *page.Title, Revision: page.Revision, CanEdit: *page.CanEdit, canvasSnapshotGraph: page.canvasSnapshotGraph}
		} else {
			if page.Revision != snapshot.Revision || *page.Title != snapshot.Title || *page.CanEdit != snapshot.CanEdit {
				return nil, invalidCanvasSnapshot()
			}
			snapshot.Groups = append(snapshot.Groups, page.Groups...)
			snapshot.Nodes = append(snapshot.Nodes, page.Nodes...)
			snapshot.Connections = append(snapshot.Connections, page.Connections...)
		}

		cursor := page.NextCursor
		if cursor == "" {
			if !snapshot.canvasSnapshotGraph.valid() {
				return nil, invalidCanvasSnapshot()
			}
			return snapshot, nil
		}
		if len(cursor) > maxCanvasSnapshotCursor || seenCursors[cursor] {
			return nil, invalidCanvasSnapshot()
		}
		seenCursors[cursor] = true
		path = basePath + "?cursor=" + url.QueryEscape(cursor)
	}
	return nil, canvasSnapshotTooLarge()
}

func invalidCanvasSnapshot() *failure {
	return fail("invalid_response", "The site returned an incomplete or inconsistent canvas snapshot. Retry the same command.")
}

func canvasSnapshotTooLarge() *failure {
	return fail("canvas_snapshot_too_large", "The canvas snapshot exceeds the CLI's size limit.")
}

func canvasSnapshotFailure(problem *failure) *failure {
	switch problem.Code {
	case "canvas_not_found", "project_not_found", "canvas_project_not_found", "cli_grant_required", "canvas_forbidden", "forbidden":
		return problem
	case "device_unauthorized", "unauthorized":
		problem.Message = "The device sign-in is invalid or expired. Sign in with ifytv auth login."
		return problem
	}
	if problem.HTTPStatus == http.StatusNotFound || problem.Code == "device_scope_denied" {
		problem.Code = "canvas_read_unavailable"
		problem.Message = "The site has not enabled canvas inspection for this device approval."
	}
	return problem
}

func (graph canvasSnapshotGraph) valid() bool {
	groups := make(map[string]bool, len(graph.Groups))
	for _, group := range graph.Groups {
		if !nodePattern.MatchString(group.ID) || group.Title == nil || groups[group.ID] {
			return false
		}
		groups[group.ID] = true
	}
	for _, group := range graph.Groups {
		if group.ParentID != "" && !groups[group.ParentID] {
			return false
		}
	}
	nodes := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if !nodePattern.MatchString(node.ID) || !validProductionID(node.Kind) || nodes[node.ID] ||
			(node.GroupID != "" && !groups[node.GroupID]) ||
			!validSnapshotSelection(node.Selection) {
			return false
		}
		for _, asset := range node.Assets {
			if !snapshotUUID(asset.ID) || !slices.Contains([]string{"image", "audio", "video", "text"}, asset.Kind) ||
				!slices.Contains([]string{"", "input", "output", "attachment"}, asset.Role) ||
				!validSnapshotSelection(asset.Selection) || (asset.Selection != "" && asset.Role != "output") {
				return false
			}
			if asset.MIMEType != "" {
				if _, _, err := mime.ParseMediaType(asset.MIMEType); err != nil || len(asset.MIMEType) > 255 {
					return false
				}
			}
		}
		if settings := node.VideoSettings; settings != nil && !settings.valid() {
			return false
		}
		if generation := node.Generation; generation != nil {
			switch generation.Status {
			case "draft", "claimed", "submitted", "uncertain", "queued", "submitting", "processing", "succeeded", "failed", "unknown":
			default:
				return false
			}
			if (generation.TaskID != "" && !snapshotUUID(generation.TaskID)) ||
				(generation.Progress != nil && (*generation.Progress < 0 || *generation.Progress > 100)) {
				return false
			}
			generation.Phase, generation.ErrorCode = safeCode(generation.Phase), safeCode(generation.ErrorCode)
		}
		nodes[node.ID] = true
	}
	connections := make(map[string]bool, len(graph.Connections))
	for _, connection := range graph.Connections {
		if !nodePattern.MatchString(connection.ID) || connections[connection.ID] ||
			!nodes[connection.SourceNodeID] || !nodes[connection.TargetNodeID] {
			return false
		}
		connections[connection.ID] = true
	}
	return true
}

func validSnapshotSelection(selection string) bool {
	return selection == "" || selection == "selected" || selection == "candidate"
}

func snapshotUUID(id string) bool {
	return validUUID(id) && strings.ToLower(id) == id
}

func (c *client) snapshotContainsCredential(data []byte) bool {
	// The device bearer and saved challenge codes are opaque ASCII strings.
	// Match the JSON representation so the check also covers escaped strings.
	for _, secret := range append([]string{c.key}, c.secrets...) {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		if bytes.Contains(data, encoded[1:len(encoded)-1]) {
			return true
		}
	}
	return false
}

func writeCanvasSnapshot(ctx context.Context, path string, data []byte) (int64, *failure) {
	temp, err := createStateTemp(filepath.Dir(path))
	if err != nil {
		return 0, fail("output_unwritable", "Cannot create a private temporary file in the output directory.")
	}
	defer os.Remove(temp.Name())
	n, err := temp.Write(data)
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if ctx.Err() != nil {
		return 0, contextFailure(ctx)
	}
	if err != nil || closeErr != nil || n != len(data) {
		return 0, fail("output_unwritable", "Cannot write the complete canvas snapshot.")
	}
	if err := fileops.Publish(temp.Name(), path, false); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, outputExists()
		}
		return 0, fail("output_unwritable", "Cannot publish the completed canvas snapshot.")
	}
	return int64(n), nil
}
