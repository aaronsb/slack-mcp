package features

import (
	"context"
	"io"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// SearchDigestForTest exposes the search digest so tests can forge cursors.
func SearchDigestForTest(query string, channels, people []string) string {
	return searchDigest(query, &searchFilters{channels: channels}, people)
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
