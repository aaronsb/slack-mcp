package features

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// DownloadFile retrieves a file attachment from a Slack message and saves
// it to the local filesystem. Defaults to ~/Downloads; a custom destDir may
// be supplied but the resolved target path must live inside that directory.
var DownloadFile = &Feature{
	Name:        "download-file",
	Description: "Download a file attachment from a Slack message. File IDs come from the 'files' field of messages returned by get-context or search.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"fileId": map[string]interface{}{
				"type":        "string",
				"description": "Slack file ID (e.g. F01234ABCD), as returned in the 'files' array of a message",
			},
			"destDir": map[string]interface{}{
				"type":        "string",
				"description": "Directory to save the file in. Defaults to ~/Downloads. Must be an absolute path.",
			},
			"filename": map[string]interface{}{
				"type":        "string",
				"description": "Override the filename (no path separators). Defaults to the original Slack filename.",
			},
		},
		"required": []string{"fileId"},
	},
	Handler: downloadFileHandler,
}

func downloadFileHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	fileID, _ := params["fileId"].(string)
	if fileID == "" {
		return &FeatureResult{
			Success: false,
			Message: "fileId is required",
		}, nil
	}

	destDir, _ := params["destDir"].(string)
	if strings.TrimSpace(destDir) == "" {
		destDir = paths.DownloadsDir()
	}

	overrideName, _ := params["filename"].(string)

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{
			Success: false,
			Message: "Internal error: provider not available",
		}, nil
	}

	api, err := apiProvider.Provide()
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to connect to Slack: %v", err),
		}, nil
	}

	file, _, _, err := api.GetFileInfoContext(ctx, fileID, 0, 0)
	if err != nil {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Failed to look up file %s: %v", fileID, err),
			Guidance: "Verify the fileId from a recent get-context or search result. External files are not downloadable.",
		}, nil
	}

	downloadURL := file.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = file.URLPrivate
	}
	if downloadURL == "" {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("File %s has no downloadable URL (external or hidden)", fileID),
			Guidance: "Slack doesn't expose a private URL for this file type — it may be hosted externally.",
		}, nil
	}

	name := overrideName
	if name == "" {
		name = file.Name
	}
	if name == "" {
		name = fileID
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Invalid filename %q: must not contain path separators", name),
		}, nil
	}

	destAbs, err := filepath.Abs(destDir)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Invalid destDir: %v", err),
		}, nil
	}
	targetAbs, err := filepath.Abs(filepath.Join(destAbs, name))
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Invalid target path: %v", err),
		}, nil
	}
	rel, err := filepath.Rel(destAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Refusing to write outside destDir: %s", targetAbs),
			Guidance: "Path traversal blocked. Pick a filename without '..' or '/'.",
		}, nil
	}

	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to create destDir %s: %v", destAbs, err),
		}, nil
	}

	out, err := os.Create(targetAbs)
	if err != nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Failed to create file %s: %v", targetAbs, err),
		}, nil
	}
	defer out.Close()

	internal := apiProvider.ProvideInternalClient()
	n, err := internal.DownloadFile(ctx, downloadURL, out)
	if err != nil {
		os.Remove(targetAbs)
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Download failed: %v", err),
		}, nil
	}

	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Downloaded %s (%d bytes) to %s", file.Name, n, targetAbs),
		Data: map[string]interface{}{
			"fileId":   fileID,
			"name":     file.Name,
			"mimetype": file.Mimetype,
			"size":     n,
			"path":     targetAbs,
		},
		NextActions: []string{
			fmt.Sprintf("Read the file at %s", targetAbs),
		},
	}, nil
}
