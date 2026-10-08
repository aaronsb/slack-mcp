package features

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
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

// The bounds a send time must fall in. Slack refused 60 seconds out about
// half the time in the probe; two minutes sits above its floor. 120 days is
// Slack's own limit.
const (
	scheduleMinLead = 2 * time.Minute
	scheduleMaxLead = 120 * 24 * time.Hour
)

// naiveLayouts are the accepted times with no offset.
var naiveLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// atForms names the accepted forms in every parse refusal.
const atForms = "RFC 3339 with an offset or Z ('2026-10-02T09:00:00-06:00'), a date and time with no offset ('2026-10-02T09:00', read in your Slack profile zone), or Unix seconds"

// atString is the at= parameter as the caller passed it, for the echo and
// the parser: a JSON number arrives as float64.
func atString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseAt resolves a send time. A time with no offset is read in profile
// (the person's Slack profile zone) only when system reads it as the same
// instant; otherwise it is refused, naming both zones. profile is nil when
// the profile zone is unknown, which refuses every time with no offset. The
// result is bounds-checked against now.
func parseAt(s string, now time.Time, profile, system *time.Location) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("at= is empty. Accepted: " + atForms + ".")
	}
	var at time.Time
	if isDigits(s) {
		secs, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("at=%q is not a time. Accepted: %s.", s, atForms)
		}
		at = time.Unix(secs, 0)
	} else if t, err := time.Parse(time.RFC3339, s); err == nil {
		at = t
	} else {
		layout := ""
		for _, l := range naiveLayouts {
			if _, err := time.Parse(l, s); err == nil {
				layout = l
				break
			}
		}
		if layout == "" {
			return time.Time{}, fmt.Errorf("at=%q is not a time. Accepted: %s.", s, atForms)
		}
		inSystem, _ := time.ParseInLocation(layout, s, system)
		if profile == nil {
			return time.Time{}, fmt.Errorf("at=%q has no offset, and your Slack profile zone could not be read to check it against this machine's zone (%s). Give an offset, e.g. '%s'.",
				s, zoneName(system, inSystem), inSystem.Format(time.RFC3339))
		}
		inProfile, _ := time.ParseInLocation(layout, s, profile)
		if !inProfile.Equal(inSystem) {
			return time.Time{}, fmt.Errorf("at=%q has no offset, and your Slack profile zone (%s, %s) and this machine's zone (%s, %s) disagree at that time. Give an offset: '%s' or '%s'.",
				s, zoneName(profile, inProfile), offsetText(inProfile), zoneName(system, inSystem), offsetText(inSystem),
				inProfile.Format(time.RFC3339), inSystem.Format(time.RFC3339))
		}
		at = inProfile
	}
	if lead := at.Sub(now); lead < scheduleMinLead {
		return time.Time{}, fmt.Errorf("at= resolves to %s, %s; a scheduled send must be at least 2 minutes out (Slack refuses closer times).",
			at.UTC().Format("2006-01-02 15:04:05 UTC"), relative(at, now))
	} else if lead > scheduleMaxLead {
		return time.Time{}, fmt.Errorf("at= resolves to %s, %s; Slack schedules at most 120 days out.",
			at.UTC().Format("2006-01-02 15:04 UTC"), relative(at, now))
	}
	return at, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// zoneName is a location's IANA name, or its abbreviation at t for Local.
func zoneName(loc *time.Location, t time.Time) string {
	if name := loc.String(); name != "Local" && name != "" {
		return name
	}
	abbr, _ := t.Zone()
	return abbr
}

func offsetText(t time.Time) string {
	return "UTC" + t.Format("-07:00")
}

// relative is the distance from now: "in 14h 12m", "in 3d 2h", "2m ago".
func relative(at, now time.Time) string {
	d := at.Sub(now)
	ago := d < 0
	if ago {
		d = -d
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d % (24 * time.Hour) / time.Hour)
	mins := int(d % time.Hour / time.Minute)
	var s string
	switch {
	case days > 0:
		s = fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		s = fmt.Sprintf("%dh %dm", hours, mins)
	default:
		s = fmt.Sprintf("%dm", mins)
	}
	if ago {
		return s + " ago"
	}
	return "in " + s
}

// zones are where a scheduled time renders: the profile zone (the system
// zone when the profile's is unknown), and the system zone beside it when
// its offset differs at that time.
type zones struct {
	profile *time.Location
	system  *time.Location
}

// render is a scheduled time as ADR-016 states it: absolute in the profile
// zone with abbreviation and offset, the system zone when it differs, UTC,
// and the distance from now.
func (z zones) render(at, now time.Time) string {
	primary := z.profile
	if primary == nil {
		primary = z.system
	}
	p := at.In(primary)
	s := fmt.Sprintf("%s %s (%s)", p.Format("Mon 2006-01-02 15:04"), zoneAbbr(p), offsetText(p))
	if z.profile != nil {
		if sys := at.In(z.system); sys.Format("-0700") != p.Format("-0700") {
			s += fmt.Sprintf(", %s %s on this machine", sys.Format("15:04"), zoneAbbr(sys))
		}
	}
	if p.Format("-0700") != "+0000" {
		s += ", " + at.UTC().Format("15:04") + " UTC"
	}
	return s + " — " + relative(at, now)
}

func zoneAbbr(t time.Time) string {
	abbr, _ := t.Zone()
	return abbr
}

// profileZone is the person's Slack profile zone, or nil when it cannot be
// read. A cached self user answers without a call.
func profileZone(ctx context.Context, ap *provider.ApiProvider) *time.Location {
	self := ap.SelfUserID()
	if self == "" {
		return nil
	}
	u, err := ap.ResolveUser(ctx, self)
	if err != nil || u.TZ == "" {
		return nil
	}
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		log.Printf("schedule: profile zone %q not loadable: %v", u.TZ, err)
		return nil
	}
	return loc
}

// systemZone is the server's local zone; a variable so tests pin it.
var systemZone = func() *time.Location { return time.Local }

// scheduleNow is the clock the bounds and distances read; tests pin it.
var scheduleNow = time.Now

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
	if len(out.Warnings) > 0 {
		res.Data.(map[string]interface{})["warnings"] = out.Warnings
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
	log.Printf("schedule: drafts.create failed: %v", err)
	return &FeatureResult{Success: false, Message: "Could not reach Slack to schedule the message. Nothing was scheduled.", Guidance: "Retry; check messages scheduled=true first so it is not scheduled twice."}
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
				"items": []map[string]interface{}{}, "narrowedTo": narrowName, "active": 0,
			}}, nil
		}
	}

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
		pruneScheduled(ap, drafts)
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
			"active":     len(drafts),
			"hasMore":    hasMore,
		},
	}, nil
}

// cancelScheduled is say cancel=: it deletes the draft a scheduled handle
// names, and only when that draft is still a pending scheduled message.
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
	drafts, _, err := ic.ListDrafts(ctx)
	if err != nil {
		log.Printf("schedule: drafts.list before cancel failed: %v", err)
		return &FeatureResult{Success: false, Message: "Could not read the scheduled messages from Slack. Nothing was cancelled.", Guidance: "Retry in a moment."}, nil
	}
	var target *provider.Draft
	for i := range drafts {
		if drafts[i].ID == ref.TS {
			target = &drafts[i]
			break
		}
	}
	if target == nil || !provider.IsScheduled(*target) || target.Destination().ChannelID != ref.Channel {
		return &FeatureResult{
			Success:  false,
			Message:  "No pending scheduled message for that handle (already sent, cancelled, or never scheduled). Nothing was cancelled.",
			Guidance: "See what's scheduled: messages scheduled=true",
		}, nil
	}
	if err := ic.DeleteDraft(ctx, target.ID, target.LastUpdatedTS); err != nil {
		log.Printf("schedule: drafts.delete failed: %v", err)
		return &FeatureResult{Success: false, Message: "Slack did not cancel the scheduled message; it is still scheduled.", Guidance: "Retry, or cancel it from Slack's Scheduled list."}, nil
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
