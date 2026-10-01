package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestIsSlackHostIsASCIIOnly(t *testing.T) {
	for _, ok := range []string{"slack.com", "files.slack.com", "FILES.SLACK.COM", "a-b.slack.com", "team.enterprise.slack.com"} {
		if !IsSlackHost(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{
		"", ".slack.com", "evilslack.com", "slack.com.evil.example", "files.slack.co",
		"files.slacK.com", // KELVIN SIGN, which Unicode folding maps to 'k'
		"files.ſlack.com", // LATIN SMALL LETTER LONG S, which folds to 's'
		"xn--files-slack.com", "files.slack.com.", "files_slack.com", "files.slack.com:443",
		"files.slack.com\x00", "files .slack.com",
	} {
		if IsSlackHost(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckSlackURL(t *testing.T) {
	for _, ok := range []string{"https://files.slack.com/x", "https://slack.com:443/api/"} {
		u, _ := url.Parse(ok)
		if err := CheckSlackURL(u); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://files.slack.com/x",
		"https://u@files.slack.com/x",
		"https://u:p@files.slack.com/x",
		"https://files.slack.com:8443/x",
		"https://files.slack.com:80/x",
		"https://files.slacK.com/x",
		"https://example.com/x",
	} {
		u, err := url.Parse(bad)
		if err != nil {
			continue // unparseable is refused upstream
		}
		if err := CheckSlackURL(u); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRedirectPolicyRefusesForeignHops(t *testing.T) {
	hop := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	slackOnly := redirectPolicy("")
	if err := slackOnly(hop("https://files.slack.com/x"), nil); err != nil {
		t.Fatalf("slack hop refused: %v", err)
	}
	for _, bad := range []string{"https://example.com/x", "http://files.slack.com/x", "https://files.slacK.com/x", "http://127.0.0.1:9999/x"} {
		if err := slackOnly(hop(bad), nil); err == nil {
			t.Errorf("hop to %s allowed", bad)
		}
	}

	fake := redirectPolicy("http://127.0.0.1:9999")
	if err := fake(hop("http://127.0.0.1:9999/api/x"), nil); err != nil {
		t.Fatalf("base host hop refused: %v", err)
	}
	for _, bad := range []string{"http://127.0.0.1:9998/x", "https://127.0.0.1:9999/x", "http://u@127.0.0.1:9999/x"} {
		if err := fake(hop(bad), nil); err == nil {
			t.Errorf("hop to %s allowed with a base URL", bad)
		}
	}
	if err := slackOnly(hop("https://files.slack.com/x"), make([]*http.Request, 10)); err == nil {
		t.Fatalf("eleventh hop allowed")
	}
}

func TestDownloadFileRefusesLookalikeAndNonDefaultHosts(t *testing.T) {
	c := NewInternalClient("xoxc-test", "xoxd-test")
	for _, bad := range []string{
		"https://files.slacK.com/files-pri/x",
		"https://u@files.slack.com/files-pri/x",
		"https://files.slack.com:8443/files-pri/x",
		"http://files.slack.com/files-pri/x",
	} {
		var buf bytes.Buffer
		_, err := c.DownloadFile(context.Background(), bad, &buf)
		if err == nil || !strings.Contains(err.Error(), "refusing to download") {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}
