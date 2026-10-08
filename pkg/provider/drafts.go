package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
)

// The drafts API (ADR-016): Slack's own scheduled send, reachable with a
// session token where chat.scheduleMessage is not. It stores the person's
// unsent composer drafts beside scheduled ones; IsScheduled is the one
// predicate that tells them apart, and nothing outside this file decides it.

// DraftDestination is where a draft goes. Broadcast is the key the drafts
// API reads ("reply_broadcast" is dropped, ADR-003's endpoint table).
type DraftDestination struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Broadcast bool   `json:"broadcast,omitempty"`
}

// Draft is one drafts.list or drafts.create item.
type Draft struct {
	ID            string             `json:"id"`
	DateScheduled int64              `json:"date_scheduled"`
	IsSent        bool               `json:"is_sent"`
	IsDeleted     bool               `json:"is_deleted"`
	LastUpdatedTS string             `json:"last_updated_ts"`
	Destinations  []DraftDestination `json:"destinations"`
	Blocks        json.RawMessage    `json:"blocks"`
}

// IsScheduled reports whether d is a pending scheduled message, as opposed
// to a composer draft the person typed or a message already sent.
func IsScheduled(d Draft) bool {
	return d.DateScheduled > 0 && !d.IsSent && !d.IsDeleted
}

// Destination is the draft's first destination; drafts the server creates
// have exactly one.
func (d Draft) Destination() DraftDestination {
	if len(d.Destinations) == 0 {
		return DraftDestination{}
	}
	return d.Destinations[0]
}

// RichText decodes the draft's blocks into rich_text blocks, skipping any
// other kind. A draft whose blocks do not decode yields none.
func (d Draft) RichText() []*slack.RichTextBlock {
	var raw []json.RawMessage
	if err := json.Unmarshal(d.Blocks, &raw); err != nil {
		return nil
	}
	var out []*slack.RichTextBlock
	for _, b := range raw {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &head) != nil || head.Type != string(slack.MBTRichText) {
			continue
		}
		rt := &slack.RichTextBlock{}
		if err := rt.UnmarshalJSON(b); err == nil {
			out = append(out, rt)
		}
	}
	return out
}

// DraftError is a drafts call Slack answered with ok=false. Code is Slack's
// error string, so callers can match draft_has_conflict,
// attached_draft_exists, and time_in_past.
type DraftError struct {
	Method string
	Code   string
}

func (e *DraftError) Error() string { return e.Method + ": " + e.Code }

type draftEnvelope struct {
	OK      bool    `json:"ok"`
	Error   string  `json:"error"`
	Draft   Draft   `json:"draft"`
	Drafts  []Draft `json:"drafts"`
	HasMore bool    `json:"has_more"`
}

func (c *InternalClient) drafts(ctx context.Context, method string, form url.Values) (*draftEnvelope, error) {
	var env draftEnvelope
	if err := c.PostFormInternalAPI(ctx, "/api/"+method, form, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		return nil, &DraftError{Method: method, Code: env.Error}
	}
	return &env, nil
}

// CreateScheduledDraft schedules blocks to dest at the given time. The
// caller has run every outbound check; this sends.
func (c *InternalClient) CreateScheduledDraft(ctx context.Context, dest DraftDestination, blocks []slack.Block, at time.Time) (Draft, error) {
	blocksJSON, err := json.Marshal(blocks)
	if err != nil {
		return Draft{}, fmt.Errorf("encoding blocks: %w", err)
	}
	destJSON, err := json.Marshal([]DraftDestination{dest})
	if err != nil {
		return Draft{}, fmt.Errorf("encoding destination: %w", err)
	}
	form := url.Values{
		"blocks":           {string(blocksJSON)},
		"destinations":     {string(destJSON)},
		"client_msg_id":    {uuid.NewString()},
		"file_ids":         {"[]"},
		"is_from_composer": {"true"},
		"date_scheduled":   {strconv.FormatInt(at.Unix(), 10)},
	}
	env, err := c.drafts(ctx, "drafts.create", form)
	if err != nil {
		return Draft{}, err
	}
	return env.Draft, nil
}

// ListDrafts returns the active drafts, scheduled and typed alike, with
// Slack's has_more: the endpoint has no cursor, so a cut list cannot be
// continued.
func (c *InternalClient) ListDrafts(ctx context.Context) ([]Draft, bool, error) {
	env, err := c.drafts(ctx, "drafts.list", url.Values{"is_active": {"true"}, "limit": {"100"}})
	if err != nil {
		return nil, false, err
	}
	return env.Drafts, env.HasMore, nil
}

// DeleteDraft deletes a draft given the last_updated_ts it was listed with.
// drafts.delete wants that value padded to seven decimals; on
// draft_has_conflict it is retried exactly once with the current time.
func (c *InternalClient) DeleteDraft(ctx context.Context, id, lastUpdatedTS string) error {
	del := func(ts string) error {
		_, err := c.drafts(ctx, "drafts.delete", url.Values{"draft_id": {id}, "client_last_updated_ts": {ts}})
		return err
	}
	err := del(PadDraftTS(lastUpdatedTS))
	if de := (*DraftError)(nil); errors.As(err, &de) && de.Code == "draft_has_conflict" {
		return del(nowDraftTS(time.Now()))
	}
	return err
}

// PadDraftTS pads a Slack timestamp's fraction to the seven digits
// drafts.delete wants.
func PadDraftTS(ts string) string {
	whole, frac, _ := strings.Cut(ts, ".")
	for len(frac) < 7 {
		frac += "0"
	}
	return whole + "." + frac
}

func nowDraftTS(t time.Time) string {
	return fmt.Sprintf("%d.%07d", t.Unix(), t.Nanosecond()/100)
}
