package features_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/setup"
)

var unlockLink = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/unlock/[0-9a-f]{32}`)

func callUnlock(t *testing.T, f *safetyFake) *features.FeatureResult {
	t.Helper()
	t.Setenv(setup.NoBrowserEnv, "1")
	res, err := features.Unlock.Handler(context.Background(), map[string]any{"_provider": f.ap})
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	return res
}

// After a block, a refusal offers unlock; unlock returns a link and says
// nothing about what will be cleared; the page names the quarantine by
// name, and Clear there lifts it.
func TestUnlockOpensAPageTheOperatorClearsOn(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	tok := fakeToken()
	f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "token " + tok})
	again := f.say(t, context.Background(), map[string]any{"to": "#eng", "text": "harmless"})
	wantIn(t, again.Guidance, "call unlock and give them the link")

	res := callUnlock(t, f)
	link := unlockLink.FindString(res.Message)
	if !res.Success || link == "" {
		t.Fatalf("no link: %+v", res)
	}
	for _, s := range []string{res.Message, res.Guidance} {
		if strings.Contains(s, "#eng") || strings.Contains(strings.ToLower(s), "cleared ") {
			t.Fatalf("result reports the state or an outcome: %s", s)
		}
	}

	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantIn(t, string(body), "#eng", "A Slack token in the message text, sending to #eng")
	if strings.Contains(string(body), "C1") || strings.Contains(string(body), tok) {
		t.Fatalf("page shows an ID or the value:\n%s", body)
	}

	origin := "http://127.0.0.1:" + unlockLink.FindStringSubmatch(link)[1]
	// Row 0 is the strike count (1 of 2), row 1 is #eng.
	req, _ := http.NewRequest(http.MethodPost, link, strings.NewReader(url.Values{"action": {"clear"}, "row": {"1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if st := f.workspace(t).Quarantine.State(); len(st.Conversations) != 0 || st.Strikes != 1 {
		t.Fatalf("after clearing #eng: %+v", st)
	}
}

// A second unlock stops the first page.
func TestUnlockReplacesTheEarlierPage(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	first := unlockLink.FindString(callUnlock(t, f).Message)
	second := unlockLink.FindString(callUnlock(t, f).Message)
	if first == "" || second == "" || first == second {
		t.Fatalf("links: %q %q", first, second)
	}
	if resp, err := http.Get(first); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("the earlier page still answers")
		}
	}
	if resp, err := http.Get(second); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the new page does not answer: %v", err)
	} else {
		resp.Body.Close()
	}
}

func TestUnlockRefusedOnRemoteDeployment(t *testing.T) {
	f := newSafetyFake(t, strictHuman())
	t.Setenv("SLACK_MCP_DEPLOYMENT", "remote")
	res := callUnlock(t, f)
	if res.Success || unlockLink.MatchString(res.Message) {
		t.Fatalf("remote unlock opened a page: %+v", res)
	}
	wantIn(t, res.Message, "remote deployment")
}

func TestInstructionsOfferUnlock(t *testing.T) {
	wantIn(t, features.Instructions(safety.Org{}, ""), "call unlock and give them the link")
}
