package features

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/text"
	"github.com/slack-go/slack"
)

// Scheduled send (ADR-016): say at= schedules through Slack's drafts API,
// messages scheduled=true lists what is pending, say cancel= withdraws one.
// Every scheduled send passes the same outbound steps as an immediate one
// (ADR-013) before the draft is created.

// destLabel names a conversation for output, never by ID: the cache's name,
// then conversations.info, then a description.
func destLabel(ctx context.Context, ap *provider.ApiProvider, id string) string {
	if where, named := conversationLabel(ap, id); named {
		return where
	}
	if name := ap.ResolveChannelName(ctx, id); name != "" && name != id {
		return "#" + name
	}
	return "a conversation not in the cache"
}

// scheduleSend is say text at=: the outbound steps, then drafts.create.
func scheduleSend(ctx context.Context, ap *provider.ApiProvider, params map[string]interface{}, at time.Time, z zones) (*FeatureResult, error) {
	to, _ := params["to"].(string)
	msg, _ := params["text"].(string)
	thread, _ := params["thread"].(string)
	broadcast, _ := params["broadcast"].(bool)

	rt := text.ToRichText(msg)
	if len(rt.Elements) == 0 {
		return &FeatureResult{Success: false, Message: "The text has nothing to schedule once formatted. Nothing was scheduled."}, nil
	}

	dest, terr := resolveWriteDestination(ctx, ap, to, "to")
	if terr != nil {
		return terr.result(), nil
	}
	out := &outbound{Thread: thread, Broadcast: broadcast, Text: msg, Fallback: text.NormalizeMrkdwn(msg), RichText: rt, At: at}
	if refusal := preSend(ctx, ap, dest, out); refusal != nil {
		return refusal, nil
	}
	channelID, terr := openDestination(ctx, ap, dest)
	if terr != nil {
		return terr.result(), nil
	}

	draft, err := ap.ProvideInternalClient().CreateScheduledDraft(ctx,
		provider.DraftDestination{ChannelID: channelID, ThreadTS: thread, Broadcast: broadcast},
		[]slack.Block{rt}, at)
	if err != nil {
		return scheduleError(err), nil
	}
	recordScheduled(ap, draft)

	now := scheduleNow()
	where := dest.Name
	if where == "" || where == namedByCaller {
		where = destLabel(ctx, ap, channelID)
	}
	h := handle.Scheduled(channelID, draft.ID)
	res := &FeatureResult{
		Success: true,
		Message: "Scheduled " + where,
		Data: map[string]interface{}{
			"destination": where,
			"thread":      thread,
			"broadcast":   broadcast,
			"when":        z.render(at, now),
			"at":          at.UTC().Format(time.RFC3339),
			"preview":     truncate(msg, 120),
			"handle":      h,
			"account":     sentPhrase(ap),
		},
		NextActions: []string{
			"See what's scheduled: messages scheduled=true",
			fmt.Sprintf("Cancel it: say cancel='%s'", h),
		},
	}
	return res, nil
}

// scheduleError turns a drafts.create failure into a result; nothing was
// scheduled.
func scheduleError(err error) *FeatureResult {
	var de *provider.DraftError
	if errors.As(err, &de) {
		switch de.Code {
		case "attached_draft_exists":
			return &FeatureResult{
				Success:  false,
				Message:  "Slack refused: that conversation has an unsent draft in your composer (attached_draft_exists). Nothing was scheduled.",
				Guidance: "Ask the operator to send or clear the composer draft in Slack, or schedule into a thread with thread='<ts>'.",
			}
		case "time_in_past":
			return &FeatureResult{
				Success:  false,
				Message:  "Slack refused the send time as too close (time_in_past). Nothing was scheduled.",
				Guidance: "Pick a time a few minutes further out.",
			}
		}
		log.Printf("schedule: drafts.create refused: %s", de.Code)
		return &FeatureResult{Success: false, Message: fmt.Sprintf("Slack refused the schedule (%s). Nothing was scheduled.", de.Code)}
	}
	// The request may have reached Slack: a lost or unreadable response
	// says nothing about whether the draft exists.
	log.Printf("schedule: drafts.create failed: %v", err)
	return &FeatureResult{Success: false, Message: "Could not confirm with Slack whether the message was scheduled.", Guidance: "Check messages scheduled=true before retrying, so it is not scheduled twice."}
}

// listScheduled is messages scheduled=true: pending scheduled drafts,
// soonest first, narrowed to one conversation by target=. It reads only;
// it never marks anything read and never writes a draft.
func listScheduled(ctx context.Context, ap *provider.ApiProvider, target string) (*FeatureResult, error) {
	narrow, narrowName := "", ""
	if target != "" {
		dest, terr := locateTarget(ctx, ap, conversationOf(target), provider.ReadPolicy, "target")
		if terr != nil {
			return terr.result(), nil
		}
		narrow, narrowName = dest.ConvID, dest.Name
		if narrow == "" {
			// A person with no DM has nothing scheduled to them: a draft
			// needs the conversation to exist.
			return &FeatureResult{Success: true, Message: "scheduled", Data: map[string]interface{}{
				"items": []map[string]interface{}{}, "narrowedTo": narrowName,
			}}, nil
		}
	}

	listedAt := time.Now()
	drafts, hasMore, err := ap.ProvideInternalClient().ListDrafts(ctx)
	if err != nil {
		log.Printf("schedule: drafts.list failed: %v", err)
		return &FeatureResult{Success: false, Message: "Could not read the scheduled messages from Slack.", Guidance: "Retry in a moment."}, nil
	}

	var pending []provider.Draft
	for _, d := range drafts {
		if !provider.IsScheduled(d) {
			continue
		}
		if narrow != "" && d.Destination().ChannelID != narrow {
			continue
		}
		pending = append(pending, d)
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].DateScheduled < pending[j].DateScheduled })

	recs := scheduledRecords(ap)
	if !hasMore {
		pruneScheduled(ap, drafts, listedAt)
	}

	z := zones{profile: profileZone(ctx, ap), system: systemZone()}
	now := scheduleNow()
	items := make([]map[string]interface{}, 0, len(pending))
	for _, d := range pending {
		dest := d.Destination()
		at := time.Unix(d.DateScheduled, 0)
		var blocks []slack.Block
		for _, rt := range d.RichText() {
			blocks = append(blocks, rt)
		}
		items = append(items, map[string]interface{}{
			"when":        z.render(at, now),
			"destination": destLabel(ctx, ap, dest.ChannelID),
			"thread":      dest.ThreadTS,
			"broadcast":   dest.Broadcast,
			"preview":     truncate(text.RichTextToMrkdwn(blocks), 80),
			"origin":      originOf(recs, d, z, now),
			"handle":      handle.Scheduled(dest.ChannelID, d.ID),
		})
	}
	return &FeatureResult{
		Success: true,
		Message: "scheduled",
		Data: map[string]interface{}{
			"items":      items,
			"narrowedTo": narrowName,
			"hasMore":    hasMore,
		},
	}, nil
}

// cancelScheduled is say cancel=: it deletes the draft a scheduled handle
// names, and only while that draft is a pending scheduled message in the
// handle's conversation. A conflict means the draft changed since it was
// listed (an edit in Slack, or it fired): it is listed and checked again,
// and deleted at most once more.
func cancelScheduled(ctx context.Context, ap *provider.ApiProvider, h string) (*FeatureResult, error) {
	ref, err := handle.Decode(h)
	if err != nil || ref.Kind != handle.KindScheduled {
		return &FeatureResult{
			Success:  false,
			Message:  "cancel= takes a scheduled message's handle, from say at= or messages scheduled=true. Nothing was cancelled.",
			Guidance: "See what's scheduled: messages scheduled=true",
		}, nil
	}
	ic := ap.ProvideInternalClient()
	var target provider.Draft
	for attempt := 0; ; attempt++ {
		found, refusal := findPending(ctx, ic, ref)
		if refusal != nil {
			if attempt > 0 {
				refusal.Message = "The scheduled message changed while it was being cancelled and is no longer pending (it may have just been sent). Nothing was cancelled."
			}
			return refusal, nil
		}
		target = found
		err = ic.DeleteDraft(ctx, target.ID, target.LastUpdatedTS)
		var de *provider.DraftError
		if err == nil || attempt > 0 || !errors.As(err, &de) || de.Code != "draft_has_conflict" {
			break
		}
	}
	if err != nil {
		log.Printf("schedule: drafts.delete failed: %v", err)
		var de *provider.DraftError
		msg := "Could not confirm with Slack whether the message was cancelled."
		if errors.As(err, &de) {
			msg = fmt.Sprintf("Slack refused the cancel (%s); the message is still scheduled.", de.Code)
		}
		return &FeatureResult{Success: false, Message: msg, Guidance: "Check messages scheduled=true, or cancel it from Slack's Scheduled list."}, nil
	}
	forgetScheduled(ap, target.ID)

	z := zones{profile: profileZone(ctx, ap), system: systemZone()}
	var blocks []slack.Block
	for _, rt := range target.RichText() {
		blocks = append(blocks, rt)
	}
	return &FeatureResult{
		Success: true,
		Message: "Cancelled",
		Data: map[string]interface{}{
			"destination": destLabel(ctx, ap, ref.Channel),
			"when":        z.render(time.Unix(target.DateScheduled, 0), scheduleNow()),
			"preview":     truncate(text.RichTextToMrkdwn(blocks), 120),
		},
	}, nil
}

// findPending lists drafts and returns the one ref names, if it is still a
// pending scheduled message in ref's conversation.
func findPending(ctx context.Context, ic *provider.InternalClient, ref handle.Ref) (provider.Draft, *FeatureResult) {
	drafts, hasMore, err := ic.ListDrafts(ctx)
	if err != nil {
		log.Printf("schedule: drafts.list before cancel failed: %v", err)
		return provider.Draft{}, &FeatureResult{Success: false, Message: "Could not read the scheduled messages from Slack. Nothing was cancelled.", Guidance: "Retry in a moment."}
	}
	for _, d := range drafts {
		if d.ID == ref.TS && provider.IsScheduled(d) && d.Destination().ChannelID == ref.Channel {
			return d, nil
		}
	}
	msg := "No pending scheduled message for that handle (already sent, cancelled, or never scheduled). Nothing was cancelled."
	if hasMore {
		msg = "That scheduled message is not among the drafts Slack returned, and Slack cut its list with no next page, so it may exist beyond it. Nothing was cancelled."
	}
	return provider.Draft{}, &FeatureResult{Success: false, Message: msg, Guidance: "See what's scheduled: messages scheduled=true"}
}
