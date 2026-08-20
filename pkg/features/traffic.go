package features

import (
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// observeTraffic reports fetched messages to the attention ledger — the
// encounter observer riding a fetch that already happened (ADR-008 stage
// 2). Only author and timestamp travel; text stays behind. Best-effort by
// contract: the provider drops the observation when no ledger is open.
func observeTraffic(ap *provider.ApiProvider, conv string, msgs []slack.Message) {
	if ap == nil || conv == "" || len(msgs) == 0 {
		return
	}
	samples := make([]provider.EncounterSample, 0, len(msgs))
	for _, m := range msgs {
		samples = append(samples, provider.EncounterSample{User: m.User, TS: m.Timestamp})
	}
	ap.ObserveEncounters(conv, samples)
}
