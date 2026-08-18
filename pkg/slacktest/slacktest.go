// Package slacktest provides a fake Slack host for testing.
//
// It serves both the public API that slack-go calls and the internal endpoints
// (client.counts, subscriptions.thread.getView) that this server reads directly.
// Response shapes are taken from real responses captured against a live
// workspace, including the fields ADR-003 depends on — `latest` on every
// conversation whether read or unread, and `latest_reply` on thread roots.
//
// Typical use:
//
//	srv := slacktest.New(t)
//	srv.Handle("client.counts", func(*http.Request) any { ... })
//	p := srv.Provider(t)
package slacktest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/paths"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/slack-go/slack"
)

// Handler produces the JSON body for one endpoint. Returning nil falls through
// to the default fixture.
type Handler func(r *http.Request) any

// Server is a fake Slack host.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	handlers map[string]Handler
	calls    map[string]int

	channels []slack.Channel
	users    []slack.User
}

// New starts a fake Slack host serving default fixtures. It is closed when the
// test finishes.
func New(t *testing.T) *Server {
	t.Helper()

	// Keep the cache store out of the user's real XDG data directory.
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	s := &Server{
		handlers: make(map[string]Handler),
		calls:    make(map[string]int),
		channels: []slack.Channel{
			memberChannel("C1", "eng"),
			memberChannel("C2", "platform"),
		},
		users: []slack.User{
			{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
			{ID: "U2", Name: "schen", RealName: "Sarah Chen"},
		},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Server.Close)
	return s
}

// SeedChannels replaces the channels a provider starts warm with.
func (s *Server) SeedChannels(channels ...slack.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = channels
}

// SeedUsers replaces the users a provider starts warm with.
func (s *Server) SeedUsers(users ...slack.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = users
}

func memberChannel(id, name string) slack.Channel {
	var ch slack.Channel
	ch.ID = id
	ch.Name = name
	ch.IsChannel = true
	ch.IsMember = true
	return ch
}

// Handle overrides the response for one endpoint, named without the "/api/"
// prefix (e.g. "client.counts").
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// Calls reports how many times an endpoint was requested. Use it to assert the
// cost of a tick, not only its output.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// ResetCalls zeroes the call counters, so a test can measure one phase in
// isolation from provider bootstrap.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = make(map[string]int)
}

// Provider returns an ApiProvider whose public and internal clients both point
// at this server, started warm.
//
// The provider loads members channels in a background goroutine (the two-phase
// cache from ADR-001), so a test that relied on that fetch would race it.
// Seeding the on-disk cache first means loadChannelsFromCache — which runs
// synchronously during bootstrap — has the fixtures before any tool runs.
func (s *Server) Provider(t *testing.T) *provider.ApiProvider {
	t.Helper()

	s.mu.Lock()
	channels := append([]slack.Channel(nil), s.channels...)
	users := append([]slack.User(nil), s.users...)
	s.mu.Unlock()

	dir := paths.DataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("slacktest: create data dir: %v", err)
	}
	writeCache(t, filepath.Join(dir, "channels.json"), channels)
	writeCache(t, filepath.Join(dir, "users.json"), users)

	return provider.NewWithTokens("xoxc-test", "xoxd-test", provider.WithBaseURL(s.URL))
}

func writeCache(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("slacktest: marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("slacktest: write %s: %v", path, err)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")

	s.mu.Lock()
	s.calls[method]++
	h := s.handlers[method]
	channels := append([]slack.Channel(nil), s.channels...)
	users := append([]slack.User(nil), s.users...)
	s.mu.Unlock()

	var body any
	if h != nil {
		body = h(r)
	}
	if body == nil {
		body = defaultFixture(method, s.URL, channels, users)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// defaultFixture returns the canned response for an endpoint. Unknown endpoints
// return a bare ok so a test fails on its assertion rather than on transport.
func defaultFixture(method, selfURL string, channels []slack.Channel, users []slack.User) any {
	switch method {
	case "auth.test":
		return map[string]any{
			"ok": true, "url": selfURL + "/", "team": "Praecipio",
			"user": "bockeliea", "team_id": "T1", "user_id": "U1",
		}

	case "users.list":
		return map[string]any{
			"ok":                true,
			"members":           users,
			"response_metadata": map[string]any{"next_cursor": ""},
		}

	case "users.conversations", "conversations.list":
		return map[string]any{
			"ok":                true,
			"channels":          channels,
			"response_metadata": map[string]any{"next_cursor": ""},
		}

	case "conversations.info":
		if len(channels) > 0 {
			return map[string]any{"ok": true, "channel": channels[0]}
		}
		return map[string]any{"ok": false, "error": "channel_not_found"}

	case "conversations.history":
		return map[string]any{
			"ok": true, "has_more": false,
			"messages": []any{Message("U2", "ship it", "1782246118.543969")},
		}

	case "conversations.replies":
		root := Message("U2", "deploy rollback?", "1782246118.543969")
		root["reply_count"] = 9
		root["latest_reply"] = "1786752114.508819"
		root["subscribed"] = true
		return map[string]any{"ok": true, "has_more": false, "messages": []any{root}}

	case "client.counts":
		return Counts(
			[]any{Conversation("C1", "1782246000.000000", "1782246118.543969", true, 1)},
			[]any{Conversation("D1", "1786752000.000000", "1786752114.508819", true, 0)},
		)

	case "subscriptions.thread.getView":
		return ThreadView(Thread("C1", "1782246118.543969", "1786752114.508819", 9, 2))

	case "conversations.mark", "chat.postMessage", "reactions.add", "reactions.remove":
		return map[string]any{"ok": true, "ts": "1786752114.508819", "channel": "C1"}

	default:
		return map[string]any{"ok": true}
	}
}

// --- fixture builders -------------------------------------------------------

// User builds a users.list member entry.
func User(id, name, realName string) map[string]any {
	return map[string]any{"id": id, "name": name, "real_name": realName}
}

// Channel builds a conversations entry the caller is a member of.
func Channel(id, name string) map[string]any {
	return map[string]any{"id": id, "name": name, "is_channel": true, "is_member": true}
}

// Message builds a conversations.history entry.
func Message(user, text, ts string) map[string]any {
	return map[string]any{"type": "message", "user": user, "text": text, "ts": ts}
}

// Conversation builds one client.counts entry. `latest` is present whether or
// not the conversation has unreads — the property ADR-003's watermark relies on.
func Conversation(id, lastRead, latest string, hasUnreads bool, mentions int) map[string]any {
	return map[string]any{
		"id": id, "last_read": lastRead, "latest": latest, "updated": latest,
		"has_unreads": hasUnreads, "mention_count": mentions,
	}
}

// Counts builds a client.counts response including the per-channel thread
// counts that thread_counts_by_channel=true adds.
func Counts(channels, ims []any) map[string]any {
	return map[string]any{
		"ok": true, "channels": channels, "ims": ims, "mpims": []any{},
		"threads": map[string]any{
			"has_unreads": true, "mention_count": 1, "vip_count": 0,
			"mention_count_by_channel": map[string]any{"C1": 1},
			"unread_count_by_channel":  map[string]any{"C1": 2},
		},
		"channel_badges": map[string]any{"channels": 1, "dms": 1},
	}
}

// Thread builds one subscriptions.thread.getView entry.
func Thread(channel, threadTs, latestReply string, replyCount, unread int) map[string]any {
	root := Message("U2", "deploy rollback?", threadTs)
	root["thread_ts"] = threadTs
	root["channel"] = channel
	root["reply_count"] = replyCount
	root["latest_reply"] = latestReply
	root["subscribed"] = true

	return map[string]any{
		"root_msg":       root,
		"latest_replies": []any{Message("U1", "on it", latestReply)},
		"unread_replies": unread,
	}
}

// ThreadView wraps threads in a getView response. Note the real endpoint caps
// at 10 entries per page and pages via current_ts; tests that care about paging
// should override the handler.
func ThreadView(threads ...any) map[string]any {
	total := 0
	for _, t := range threads {
		if m, ok := t.(map[string]any); ok {
			if n, ok := m["unread_replies"].(int); ok {
				total += n
			}
		}
	}
	return map[string]any{
		"ok": true, "has_more": false, "max_ts": "1787005990.000000",
		"new_threads_count": len(threads), "total_unread_replies": total,
		"threads": threads,
	}
}
