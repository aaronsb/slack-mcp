package features_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// ADR-010: the batch executor — one call runs a held plan of reads, each
// item rendered under its own output laws, and the read-only boundary is
// enforced by name before anything executes.

func runBatch(t *testing.T, ap *provider.ApiProvider, params map[string]any) *features.FeatureResult {
	t.Helper()
	params["_provider"] = ap
	res, err := features.Batch.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	return res
}

func batchOut(t *testing.T, ap *provider.ApiProvider, params map[string]any) string {
	t.Helper()
	return features.FormatResult("batch", runBatch(t, ap, params))
}

func twoChannels() []slack.Channel {
	general := slack.Channel{}
	general.ID = "C1"
	general.Name = "general"
	general.IsChannel = true
	general.IsMember = true
	eng := slack.Channel{}
	eng.ID = "C2"
	eng.Name = "eng"
	eng.IsChannel = true
	eng.IsMember = true
	return []slack.Channel{general, eng}
}

func TestBatchRunsCommandsInOrderAsOneDocument(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(twoChannels()...)
	ap := bootedProvider(t, srv)

	out := batchOut(t, ap, map[string]any{
		"commands": []any{
			map[string]any{"tool": "estate", "params": map[string]any{"view": "channels"}},
			map[string]any{"tool": "estate", "params": map[string]any{"view": "people", "search": "sarah"}},
		},
	})

	channelsAt := strings.Index(out, "view='channels'")
	peopleAt := strings.Index(out, "view='people'")
	if channelsAt < 0 || peopleAt < 0 {
		t.Fatalf("item echo lines missing:\n%s", out)
	}
	if channelsAt > peopleAt {
		t.Fatalf("items rendered out of order:\n%s", out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Fatalf("sections not separated by a horizontal rule:\n%s", out)
	}
	if !strings.Contains(out, "#general") {
		t.Fatalf("first item's rendered content missing:\n%s", out)
	}
}

func TestBatchRendersAFailingItemInPlaceAndContinues(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(twoChannels()...)
	ap := bootedProvider(t, srv)

	out := batchOut(t, ap, map[string]any{
		"commands": []any{
			map[string]any{"tool": "messages", "params": map[string]any{}},
			map[string]any{"tool": "estate", "params": map[string]any{"view": "channels"}},
		},
	})

	errAt := strings.Index(out, "**Error:**")
	channelsAt := strings.Index(out, "#general")
	if errAt < 0 {
		t.Fatalf("failing item's error not rendered in place:\n%s", out)
	}
	if channelsAt < 0 || channelsAt < errAt {
		t.Fatalf("execution did not continue past the failing item:\n%s", out)
	}
	if !strings.Contains(out, "`messages`") {
		t.Fatalf("failing item's echo line missing:\n%s", out)
	}
}

func TestBatchRejectsWriteToolsByName(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	for _, tool := range []string{"say", "dismiss", "mark-read", "auth", "download"} {
		res := runBatch(t, ap, map[string]any{
			"commands": []any{map[string]any{"tool": tool}},
		})
		if res.Success {
			t.Fatalf("%s was admitted to a batch", tool)
		}
		if !strings.Contains(res.Message, tool) || !strings.Contains(res.Message, "reads") {
			t.Fatalf("rejection does not name the tool and the boundary: %s", res.Message)
		}
	}
}

func TestBatchRejectsNesting(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	res := runBatch(t, ap, map[string]any{
		"commands": []any{map[string]any{"tool": "batch"}},
	})
	if res.Success || !strings.Contains(res.Message, "batch may not contain batch") {
		t.Fatalf("nesting not rejected: %+v", res)
	}
}

func TestBatchRejectsMoreThanTwelveCommands(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	cmds := make([]any, 13)
	for i := range cmds {
		cmds[i] = map[string]any{"tool": "estate", "params": map[string]any{"view": "channels"}}
	}
	res := runBatch(t, ap, map[string]any{"commands": cmds})
	if res.Success || !strings.Contains(res.Message, "12") {
		t.Fatalf("cap not enforced: %+v", res)
	}
}

func TestPlaybookSaveRunListDeleteRoundTrip(t *testing.T) {
	srv := slacktest.New(t)
	srv.SeedChannels(twoChannels()...)
	ap := bootedProvider(t, srv)

	saved := runBatch(t, ap, map[string]any{
		"save": "morning",
		"commands": []any{
			map[string]any{"tool": "estate", "params": map[string]any{"view": "channels"}},
		},
	})
	if !saved.Success {
		t.Fatalf("save failed: %s", saved.Message)
	}
	if !strings.Contains(saved.Message, "not run") {
		t.Fatalf("save must state it did not execute: %s", saved.Message)
	}

	listed := batchOut(t, ap, map[string]any{"list": true})
	if !strings.Contains(listed, "morning") || !strings.Contains(listed, "1 commands") {
		t.Fatalf("listing missing the saved playbook:\n%s", listed)
	}

	ran := batchOut(t, ap, map[string]any{"run": "morning"})
	if !strings.Contains(ran, "#general") {
		t.Fatalf("run did not execute the stored sequence:\n%s", ran)
	}

	deleted := runBatch(t, ap, map[string]any{"delete": "morning"})
	if !deleted.Success {
		t.Fatalf("delete failed: %s", deleted.Message)
	}
	missing := runBatch(t, ap, map[string]any{"run": "morning"})
	if missing.Success || !strings.Contains(missing.Message, "morning") {
		t.Fatalf("deleted playbook still runs: %+v", missing)
	}
}

func TestPlaybookSaveValidatesTheBoundaryToo(t *testing.T) {
	srv := slacktest.New(t)
	ap := bootedProvider(t, srv)

	res := runBatch(t, ap, map[string]any{
		"save":     "sneaky",
		"commands": []any{map[string]any{"tool": "say", "params": map[string]any{"to": "#general", "text": "hi"}}},
	})
	if res.Success {
		t.Fatal("a write was saved into a playbook")
	}
	after := runBatch(t, ap, map[string]any{"run": "sneaky"})
	if after.Success || !strings.Contains(after.Message, "No playbook") {
		t.Fatalf("rejected save still persisted something: %+v", after)
	}
}

func TestReadPacerHintsOnlyOnRapidConsecutiveReads(t *testing.T) {
	var p features.ReadPacer
	base := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

	if hint := p.Observe("inbox", base); hint != "" {
		t.Fatalf("first read hinted: %s", hint)
	}
	hint := p.Observe("messages", base.Add(time.Second))
	if hint == "" {
		t.Fatal("rapid second read did not hint")
	}
	if !strings.Contains(hint, "batch commands=") || !strings.Contains(hint, "inbox") || !strings.Contains(hint, "messages") {
		t.Fatalf("hint does not name the batch equivalent: %s", hint)
	}

	if hint := p.Observe("estate", base.Add(30*time.Second)); hint != "" {
		t.Fatalf("slow read hinted: %s", hint)
	}

	if hint := p.Observe("say", base.Add(31*time.Second)); hint != "" {
		t.Fatalf("a write hinted: %s", hint)
	}
	if hint := p.Observe("inbox", base.Add(32*time.Second)); hint != "" {
		t.Fatalf("read after a write hinted — the write should reset the streak: %s", hint)
	}
	if hint := p.Observe("batch", base.Add(33*time.Second)); hint != "" {
		t.Fatalf("batch itself hinted: %s", hint)
	}
}
