package features

import (
	"context"
	"io"
	"time"

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

// UploadLimits are the say files= limits a test may lower.
type UploadLimits struct {
	PerFile, Total int64
	// Delays are the files.info poll waits for the message ts.
	Delays []time.Duration
}

// SetUploadLimitsForTest replaces the say files= limits and poll waits.
// It returns a restore func.
func SetUploadLimitsForTest(l UploadLimits) func() {
	prevFile, prevTotal, prevDelays := maxUploadBytes, maxUploadTotalBytes, shareTSDelays
	maxUploadBytes, maxUploadTotalBytes, shareTSDelays = l.PerFile, l.Total, l.Delays
	return func() { maxUploadBytes, maxUploadTotalBytes, shareTSDelays = prevFile, prevTotal, prevDelays }
}

// PreSendSeen is what the preSend boundary received.
type PreSendSeen struct {
	Typed, ConvID, UserID, Name string
	Fallback                    string
	HasRichText                 bool
	Names                       []string
	Data                        [][]byte
}

// SetPreSendForTest stands in for the preSend boundary. A non-empty return
// refuses the call with that message. It returns a restore func.
func SetPreSendForTest(fn func(PreSendSeen) string) func() {
	prev := preSend
	preSend = func(_ context.Context, _ *provider.ApiProvider, dest *resolvedDestination, out *outbound) *FeatureResult {
		seen := PreSendSeen{
			Typed: dest.Typed, ConvID: dest.ConvID, UserID: dest.UserID, Name: dest.Name,
			Fallback: out.Fallback, HasRichText: out.RichText != nil,
		}
		for _, f := range out.Files {
			seen.Names = append(seen.Names, f.Name)
			seen.Data = append(seen.Data, f.Data)
		}
		if msg := fn(seen); msg != "" {
			return &FeatureResult{Success: false, Message: msg}
		}
		return nil
	}
	return func() { preSend = prev }
}

// SetPreWriteLocalForTest stands in for the preWriteLocal hook. A
// non-empty return refuses the call with that message. It returns a
// restore func.
func SetPreWriteLocalForTest(fn func() string) func() {
	prev := preWriteLocal
	preWriteLocal = func(context.Context) *FeatureResult {
		if msg := fn(); msg != "" {
			return &FeatureResult{Success: false, Message: msg}
		}
		return nil
	}
	return func() { preWriteLocal = prev }
}
