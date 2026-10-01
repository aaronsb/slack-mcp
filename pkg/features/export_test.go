package features

import (
	"context"
	"io"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// SearchDigestForTest exposes the search digest so tests can forge cursors.
func SearchDigestForTest(query string, channels, people []string) string {
	return searchDigest(query, channels, people)
}

// SetFetchFileForTest stands in for the files.slack.com fetch, which the
// internal client pins to Slack hosts. It returns a restore func.
func SetFetchFileForTest(fn func(url string, w io.Writer) (int64, error)) func() {
	prev := fetchFile
	fetchFile = func(_ context.Context, _ *provider.ApiProvider, url string, w io.Writer) (int64, error) {
		return fn(url, w)
	}
	return func() { fetchFile = prev }
}

// SetUploadLimitsForTest lowers the per-file upload cap and replaces the
// files.info poll waits for the message ts. It returns a restore func.
func SetUploadLimitsForTest(maxBytes int64, delays []time.Duration) func() {
	prevMax, prevDelays := maxUploadBytes, shareTSDelays
	maxUploadBytes, shareTSDelays = maxBytes, delays
	return func() { maxUploadBytes, shareTSDelays = prevMax, prevDelays }
}

// SetPreSendUploadForTest stands in for the pre-send boundary of say
// files=. fn sees the destination as named, the comment as sent, and each
// file's name and bytes; a non-empty refusal stops the call. It returns a
// restore func.
func SetPreSendUploadForTest(fn func(to, comment string, names []string, data [][]byte) string) func() {
	prev := preSendUpload
	preSendUpload = func(_ context.Context, _ *provider.ApiProvider, up *outboundUpload) *FeatureResult {
		names := make([]string, len(up.Files))
		data := make([][]byte, len(up.Files))
		for i, f := range up.Files {
			names[i], data[i] = f.Name, f.Data
		}
		if msg := fn(up.To, up.Comment, names, data); msg != "" {
			return &FeatureResult{Success: false, Message: msg}
		}
		return nil
	}
	return func() { preSendUpload = prev }
}
