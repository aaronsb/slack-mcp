package features

import (
	"context"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/watermark"
	"github.com/slack-go/slack"
)

// tickThreads reports thread replies the agent has not been shown.
//
// Threads need their own path because conversations.history does not return
// replies: a thread can gain twenty messages without its channel's `latest`
// moving at all, so the conversation tick is blind to it.
//
// Two sources, because only one of them is independent of the human's client:
//
//   - subscriptions.thread.getView is Slack's own Threads view. It is cheap and
//     surfaces threads the agent has never seen, but it is strictly unreads-only
//     — once the person reads a thread in their own client it vanishes from the
//     feed. It seeds the tracked set; it cannot be trusted to keep reporting.
//
//   - conversations.replies on each already-tracked thread reads latest_reply
//     directly, which no read marker affects. This is what keeps a thread
//     reporting after the person has caught up on it.
//
// The blind spot that remains, and is documented rather than hidden: a thread
// that both starts and is read in the official client between two ticks is
// never tracked, so it is never reported.
func tickThreads(
	ctx context.Context,
	api *slack.Client,
	internal *provider.InternalClient,
	store *watermark.Store,
	naming func(conversation) string,
	usersMap map[string]slack.User,
	limit int,
	now time.Time,
	t *tick,
) {
	seen := make(map[string]bool)

	// Seed from the feed.
	view, err := internal.GetThreadView(ctx, "")
	if err != nil || !view.OK {
		// The conversation half of the tick still stands; say the thread half
		// did not rather than reporting a complete tick.
		t.threadFeedFailed = true
	} else {
		for _, entry := range view.Threads {
			root := entry.RootMsg
			if root.Channel == "" || root.ThreadTS == "" {
				continue
			}
			seen[threadKey(root.Channel, root.ThreadTS)] = true
			t.threadsChecked++

			if !store.ThreadChanged(root.Channel, root.ThreadTS, root.LatestReply) {
				continue
			}
			if len(t.events) >= limit {
				t.limitReached = true
				return
			}
			t.events = append(t.events, threadEvent(
				root.Channel, root.ThreadTS, root.LatestReply,
				preview(t.render(root.Text)),
				root.ReplyCount, entry.UnreadReplies,
				naming(conversation{ID: root.Channel, Kind: "channel"}),
				getUserName(root.User, usersMap)))
		}
	}

	// Re-check what is already tracked, which the feed drops once the person
	// has read it.
	tracked := store.TrackedThreads(threadRecency, now)
	polled := 0

	for _, th := range tracked {
		if seen[threadKey(th.Channel, th.ThreadTS)] {
			continue
		}
		if polled >= maxTrackedPolled {
			t.threadsUnchecked++
			continue
		}
		polled++
		t.threadsChecked++

		root, err := threadRoot(ctx, api, th.Channel, th.ThreadTS)
		if err != nil || root == nil {
			continue
		}
		if !store.ThreadChanged(th.Channel, th.ThreadTS, root.LatestReply) {
			continue
		}
		if len(t.events) >= limit {
			t.limitReached = true
			return
		}
		rm := t.rm.Render(*root)
		t.events = append(t.events, threadEvent(
			th.Channel, th.ThreadTS, root.LatestReply,
			preview(rm.Body),
			root.ReplyCount, 0,
			naming(conversation{ID: th.Channel, Kind: "channel"}),
			rm.Author))
	}
}

// threadRoot reads a thread's root message, which carries latest_reply. One
// message is enough: the count and the newest reply timestamp are on the root.
func threadRoot(ctx context.Context, api *slack.Client, channel, threadTS string) (*slack.Message, error) {
	msgs, _, _, err := api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     1,
	})
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &msgs[0], nil
}

func threadEvent(channel, threadTS, latestReply, rootPreview string, replyCount, unread int, where, who string) map[string]interface{} {
	event := map[string]interface{}{
		// The thread handle, so read opens the whole thread rather than one
		// reply out of context.
		"handle":     handle.ThreadThrough(channel, threadTS, latestReply),
		"kind":       "thread",
		"where":      where,
		"who":        who,
		"when":       formatTimestamp(parseSlackTimestamp(latestReply)),
		"preview":    rootPreview,
		"replyCount": replyCount,
	}
	if unread > 0 {
		event["unreadReplies"] = unread
	}
	return event
}

func threadKey(channel, threadTS string) string {
	return channel + "\x00" + threadTS
}
