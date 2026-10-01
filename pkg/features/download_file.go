package features

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aaronsb/slack-mcp/pkg/exchange"
	"github.com/aaronsb/slack-mcp/pkg/lifecycle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// DownloadFile retrieves a file attachment from a Slack message and saves it
// into the exchange directory (ADR-012), the only place a file parameter can
// name. The file is created through the directory's os.Root at 0600; a taken
// name is suffixed and the rename stated.
var DownloadFile = &Feature{
	Name:        "download-file",
	Description: "Download a file attachment from a Slack message into the exchange directory. File IDs come from the 'files' field of messages.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"fileId": map[string]interface{}{
				"type":        "string",
				"description": "Slack file ID (e.g. F01234ABCD), as returned in the 'files' array of a message",
			},
			"filename": map[string]interface{}{
				"type":        "string",
				"description": "Name to save as in the exchange directory: a bare name, not a path. Defaults to the Slack filename. A taken name gets a ' (n)' suffix and the result says so.",
			},
		},
		"required": []string{"fileId"},
	},
	Handler: downloadFileHandler,
}

// fetchFile streams a Slack file URL to w. A variable so tests can stand in
// for files.slack.com, which the internal client pins.
var fetchFile = func(ctx context.Context, ap *provider.ApiProvider, url string, w io.Writer) (int64, error) {
	return ap.ProvideInternalClient().DownloadFile(ctx, url, w)
}

func downloadFileHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	fail := func(msg, guidance string) (*FeatureResult, error) {
		return &FeatureResult{Success: false, Message: msg, Guidance: guidance}, nil
	}

	// Everything the caller can get wrong is refused before any Slack call.
	if _, ok := params["destDir"]; ok {
		return fail(
			fmt.Sprintf("destDir was removed: download saves only into the exchange directory (%s). Pass filename= to choose the name.", exchange.Locate().Path),
			"Copy the file elsewhere with your own file tools after it downloads. The operator can move the exchange directory with SLACK_MCP_EXCHANGE_DIR in the MCP client config.",
		)
	}

	fileID, _ := params["fileId"].(string)
	if fileID == "" {
		return fail("fileId is required", "")
	}

	explicit, _ := params["filename"].(string)
	if explicit != "" {
		if err := exchange.ValidateName(explicit); err != nil {
			return fail(err.Error(), "Pass a bare name such as report.pdf. Nothing was downloaded.")
		}
	}

	dir, err := exchange.Open()
	if err != nil {
		return fail(err.Error(), "Nothing was downloaded.")
	}
	defer dir.Close()

	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return fail("Internal error: provider not available", "")
	}
	api, err := apiProvider.Provide()
	if err != nil {
		return fail(fmt.Sprintf("Failed to connect to Slack: %v", err), "")
	}

	file, _, _, err := api.GetFileInfoContext(ctx, fileID, 0, 0)
	if err != nil {
		return fail(fmt.Sprintf("Failed to look up file %s: %v", fileID, err),
			"Verify the fileId from a recent messages result. External files are not downloadable.")
	}
	if file.Size > provider.MaxDownloadBytes {
		return fail(fmt.Sprintf("File %s is %d bytes, exceeding the %d byte limit", fileID, file.Size, provider.MaxDownloadBytes), "")
	}

	downloadURL := file.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = file.URLPrivate
	}
	if downloadURL == "" {
		return fail(fmt.Sprintf("File %s has no downloadable URL (external or hidden)", fileID),
			"Slack doesn't expose a private URL for this file type — it may be hosted externally.")
	}

	name := explicit
	if name == "" {
		name = exchange.Sanitize(file.Name, exchange.Sanitize(fileID, ""))
		if name == "" {
			return fail(fmt.Sprintf("File %s has no usable name; pass filename=", fileID), "")
		}
	}

	created, err := dir.Create(name)
	if err != nil {
		return fail(err.Error(), "")
	}
	sum := sha256.New()
	n, err := fetchFile(ctx, apiProvider, downloadURL, io.MultiWriter(created.File, sum))
	if closeErr := created.File.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("finalizing file: %w", closeErr)
	}
	if err != nil {
		_ = dir.Remove(created.Name)
		return fail(fmt.Sprintf("Download failed: %v", err), "")
	}

	// Provenance for ADR-013's gate case 2: where Slack reports the file
	// shared, keyed by the bytes written.
	convs := append(append(append([]string(nil), file.Channels...), file.Groups...), file.IMs...)
	for _, shares := range []map[string][]slack.ShareFileInfo{file.Shares.Public, file.Shares.Private} {
		for conv := range shares {
			convs = append(convs, conv)
		}
	}
	recordProvenance(apiProvider, created.Name, fileID, hex.EncodeToString(sum.Sum(nil)), convs)

	path := dir.DisplayPath(created.Name)
	msg := fmt.Sprintf("Downloaded %s (%d bytes) into the exchange directory as %s", file.Name, n, created.Name)
	if notice := created.Notice(); notice != "" {
		msg += ". " + notice
	}
	data := map[string]interface{}{
		"fileId":   fileID,
		"name":     created.Name,
		"mimetype": file.Mimetype,
		"size":     n,
		"path":     path,
	}
	if created.Renamed() {
		data["requested"] = created.Requested
	}
	var next []string
	if dep, _ := lifecycle.DeploymentFromEnv(); dep == lifecycle.Remote {
		data["host"] = "server"
		next = append(next, "The path is on the server's host, not this client's; the operator transfers the file.")
	} else {
		next = append(next, fmt.Sprintf("Read the file at %s", path))
	}

	return &FeatureResult{
		Success:     true,
		Message:     msg,
		Data:        data,
		NextActions: next,
	}, nil
}
