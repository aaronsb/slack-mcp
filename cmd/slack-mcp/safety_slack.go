package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/safety"
	"github.com/aaronsb/slack-mcp/pkg/setup"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/aaronsb/slack-mcp/pkg/transport"
)

const cliUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

// cliTokens finds the configured tokens in the order the server uses: the
// config file's default workspace, then the environment.
func cliTokens() (xoxc, xoxd string, err error) {
	if cfg, err := setup.LoadConfig(); err == nil && len(cfg.Workspaces) > 0 {
		name := cfg.DefaultWorkspace
		if name == "" {
			for n := range cfg.Workspaces {
				name = n
				break
			}
		}
		if ws, ok := cfg.Workspaces[name]; ok {
			return ws.XoxcToken, ws.XoxdToken, nil
		}
	}
	xoxc, xoxd = os.Getenv("SLACK_MCP_XOXC_TOKEN"), os.Getenv("SLACK_MCP_XOXD_TOKEN")
	if strings.HasPrefix(xoxc, "xoxc-") && strings.HasPrefix(xoxd, "xoxd-") {
		return xoxc, xoxd, nil
	}
	return "", "", fmt.Errorf("no Slack credentials found in config (%s) or environment; run 'slack-mcp setup'", setup.ConfigPath())
}

// cliTransport builds the CLI's HTTP transport. It honors SLACK_MCP_PROXY
// and SLACK_MCP_SERVER_CA as the server does, and always verifies TLS:
// SLACK_MCP_SERVER_CA adds a root to the system pool, and
// SLACK_MCP_SERVER_CA_INSECURE is never honored, since the tokens ride on
// every request.
func cliTransport(getenv func(string) string, readFile func(string) ([]byte, error)) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if raw := getenv("SLACK_MCP_PROXY"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			// The URL may carry user:pass, so it is never printed.
			return nil, errors.New("invalid SLACK_MCP_PROXY: not a valid URL")
		}
		t.Proxy = http.ProxyURL(u)
	}
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if path := getenv("SLACK_MCP_SERVER_CA"); path != "" {
		pem, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("SLACK_MCP_SERVER_CA: %v", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("SLACK_MCP_SERVER_CA: no certificate found in the file")
		}
	}
	t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return t, nil
}

// slackDirectory resolves destinations with the configured tokens. Names
// come from the server's user and channel caches first, then Slack.
type slackDirectory struct {
	api *slack.Client
	org safety.Org

	users    []slack.User
	channels []slack.Channel
	loaded   bool
}

// connectSlackDirectory runs auth.test with the configured tokens: the team
// ID it returns keys the workspace's safety files.
func connectSlackDirectory() (directory, error) {
	xoxc, xoxd, err := cliTokens()
	if err != nil {
		return nil, err
	}
	rt, err := cliTransport(os.Getenv, os.ReadFile)
	if err != nil {
		return nil, err
	}
	if os.Getenv("SLACK_MCP_SERVER_CA_INSECURE") != "" {
		fmt.Fprintln(os.Stderr, "slack-mcp: SLACK_MCP_SERVER_CA_INSECURE is ignored here; the CLI always verifies TLS")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport.New(rt, cliUserAgent, xoxd)}
	api := slack.New(xoxc, slack.OptionHTTPClient(client))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := api.AuthTestContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth.test failed: %v", err)
	}
	if res.TeamID == "" {
		return nil, errors.New("auth.test returned no team ID")
	}
	if res.URL != "" {
		api = slack.New(xoxc, slack.OptionHTTPClient(client), slack.OptionAPIURL(res.URL+"api/"))
	}
	return &slackDirectory{api: api, org: safety.Org{TeamID: res.TeamID, EnterpriseID: res.EnterpriseID, UserID: res.UserID}}, nil
}

func (d *slackDirectory) Org() safety.Org { return d.org }

func (d *slackDirectory) loadCaches() {
	if d.loaded {
		return
	}
	d.loaded = true
	read := func(name string, dst any) {
		if raw, err := os.ReadFile(filepath.Join(paths.DataDir(), name)); err == nil {
			_ = json.Unmarshal(raw, dst)
		}
	}
	read("users.json", &d.users)
	read("channels.json", &d.channels)
}

func ctx15() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// Resolve turns @handle, #channel, or an ID into a target, with the
// external parties the gate would find now.
func (d *slackDirectory) Resolve(arg string) (cliTarget, error) {
	d.loadCaches()
	switch {
	case strings.HasPrefix(arg, "@"):
		h := strings.TrimPrefix(arg, "@")
		for _, u := range d.users {
			if strings.EqualFold(u.Name, h) {
				return d.person(u.ID)
			}
		}
		return cliTarget{}, fmt.Errorf("%s: not in the users cache; pass the user ID instead", arg)
	case strings.HasPrefix(arg, "#"):
		n := strings.TrimPrefix(arg, "#")
		for _, ch := range d.channels {
			if ch.Name == n || ch.NameNormalized == n {
				return d.conversation(ch.ID)
			}
		}
		return cliTarget{}, fmt.Errorf("%s: not in the channels cache; pass the conversation ID instead", arg)
	case strings.HasPrefix(arg, "U") || strings.HasPrefix(arg, "W"):
		return d.person(arg)
	default:
		return d.conversation(arg)
	}
}

func (d *slackDirectory) person(id string) (cliTarget, error) {
	ctx, cancel := ctx15()
	defer cancel()
	u, err := d.api.GetUserInfoContext(ctx, id)
	if err != nil {
		return cliTarget{}, fmt.Errorf("users.info %s: %v", id, err)
	}
	if u.ID == d.org.UserID {
		return cliTarget{Key: safety.Person(u.ID, "@"+u.Name), DestKind: safety.DestSelf}, nil
	}
	t := cliTarget{Key: safety.Person(u.ID, "@"+u.Name), DestKind: safety.DestPerson, External: d.org.UserExternal(u)}
	if t.External {
		t.Parties = []string{u.ID}
	}
	return t, nil
}

func (d *slackDirectory) conversation(id string) (cliTarget, error) {
	ctx, cancel := ctx15()
	defer cancel()
	ch, err := d.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: id})
	if err != nil {
		return cliTarget{}, fmt.Errorf("conversations.info %s: %v", id, err)
	}
	switch {
	case ch.IsIM:
		if ch.User == d.org.UserID {
			return cliTarget{Key: safety.Conversation(ch.ID, "@me"), DestKind: safety.DestSelf}, nil
		}
		other, err := d.person(ch.User)
		if err != nil {
			return cliTarget{}, err
		}
		t := cliTarget{Key: safety.Conversation(ch.ID, other.Key.Name), DestKind: safety.DestDM}
		t.External = ch.IsExtShared || other.External
		if t.External {
			t.Parties = []string{ch.User}
		}
		return t, nil
	case ch.IsMpIM:
		t := cliTarget{Key: safety.Conversation(ch.ID, text.GroupDMName(ch.Name, nil)), DestKind: safety.DestGroupDM, External: ch.IsExtShared}
		cursor := ""
		for {
			ids, next, err := d.api.GetUsersInConversationContext(ctx, &slack.GetUsersInConversationParameters{ChannelID: ch.ID, Cursor: cursor, Limit: 200})
			if err != nil {
				return cliTarget{}, fmt.Errorf("conversations.members %s: %v", id, err)
			}
			for _, uid := range ids {
				if uid == d.org.UserID {
					continue
				}
				m, err := d.person(uid)
				if err != nil {
					return cliTarget{}, err
				}
				if m.External {
					t.External = true
					t.Parties = append(t.Parties, uid)
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return t, nil
	default:
		t := cliTarget{Key: safety.Conversation(ch.ID, "#"+ch.Name), DestKind: safety.DestChannel, External: safety.ChannelExternal(ch)}
		if t.External {
			t.Parties = d.org.ChannelParties(ch)
		}
		return t, nil
	}
}

// CurrentName looks a key's name up at list time: the caches, then Slack.
func (d *slackDirectory) CurrentName(k safety.Key) (string, bool) {
	d.loadCaches()
	switch k.Kind {
	case safety.KeyPerson:
		for _, u := range d.users {
			if u.ID == k.ID {
				return "@" + u.Name, true
			}
		}
		ctx, cancel := ctx15()
		defer cancel()
		if u, err := d.api.GetUserInfoContext(ctx, k.ID); err == nil {
			return "@" + u.Name, true
		}
	case safety.KeyConversation:
		for _, ch := range d.channels {
			if ch.ID == k.ID && ch.Name != "" {
				return text.ChannelLabel(ch.Name, ch.IsMpIM), true
			}
		}
		ctx, cancel := ctx15()
		defer cancel()
		if ch, err := d.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: k.ID}); err == nil && ch.Name != "" {
			return text.ChannelLabel(ch.Name, ch.IsMpIM), true
		}
	}
	return "", false
}
