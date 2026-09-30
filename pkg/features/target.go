package features

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// Conversation targets: one resolver for every tool that addresses a
// conversation by what the caller typed, reads and writes alike. The prefix
// states intent and is honoured before any lookup:
//
//	'#name'   a channel, by channel name only — never a DM, not even by ID
//	C…/D…/G…  a real Slack conversation ID passes through
//	'@name'   a person, through the ADR-005 ladder
//	bare      a channel if one is named so, otherwise a person
//
// Which ladder rungs may act without asking is the caller's Policy (ADR-005):
// a read may take a unique fragment, a write only an exact handle. A bare
// word that names both a channel and a person is ambiguous to a write, which
// asks rather than guessing; a read takes the channel.

// targetError is why a target did not resolve: a message for the caller and a
// guidance line naming what to try instead. People are named by handle and
// display name, never by ID. A read-policy person miss carries the ladder's
// outcome so it renders as the since= path's miss does.
type targetError struct {
	Message  string
	Guidance string
	miss     *provider.PersonResolution
}

func (e *targetError) Error() string { return e.Message }

// result renders the failure as a FeatureResult. No Slack write has happened.
func (e *targetError) result() *FeatureResult {
	if e.miss != nil {
		return personMissResult(e.miss)
	}
	return &FeatureResult{Success: false, Message: e.Message, Guidance: e.Guidance}
}

// personMissResult is the read paths' shape for a person the ladder could
// not settle: a successful answer whose body is the candidate set.
func personMissResult(res *provider.PersonResolution) *FeatureResult {
	return &FeatureResult{
		Success:  true,
		Message:  fmt.Sprintf("Could not resolve %q (%s)", res.Input, res.Reason),
		Data:     map[string]interface{}{"view": &personViewData{Miss: res}},
		RenderAs: "estate",
	}
}

// resolveTarget maps a conversation reference to a conversation ID under a
// policy. param names the caller's parameter ('to', 'target', 'channel') so
// guidance speaks the tool's own vocabulary. A write that fails here has
// made no Slack call.
func resolveTarget(ctx context.Context, ap *provider.ApiProvider, input string, policy provider.Policy, param string) (string, *targetError) {
	in := strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(in, "#"):
		// Channels by name only: '#' never addresses a DM, not even by ID.
		name := strings.TrimSpace(strings.TrimPrefix(in, "#"))
		if ch, ok := ap.LookupChannelName(name); ok {
			return ch.ID, nil
		}
		return "", &targetError{
			Message:  fmt.Sprintf("No channel named '%s' is known, so nothing was done.", in),
			Guidance: fmt.Sprintf("See available channels: estate view='channels' — for a person, use %s='@handle'", param),
		}
	case provider.LooksLikeChannelID(in):
		return in, nil
	case strings.HasPrefix(in, "@"):
		return personTarget(ctx, ap, in, policy, param)
	}

	ch, isChannel := ap.LookupChannel(in)
	if !isChannel {
		return personTarget(ctx, ap, in, policy, param)
	}
	if policy == provider.ReadPolicy {
		return ch.ID, nil
	}
	if res := ap.ResolvePerson(in); namesPersonExactly(res, in) {
		person := "@" + in
		if res.Resolved {
			person = "@" + res.Handle
		}
		channel := "#" + in
		if ch.Name != "" {
			channel = "#" + ch.Name
		}
		return "", &targetError{
			Message:  fmt.Sprintf("'%s' names both the channel %s and the person %s, so nothing was done.", in, channel, person),
			Guidance: fmt.Sprintf("Say which: %s='%s' for the channel, or %s='%s' for a DM", param, channel, param, person),
		}
	}
	return ch.ID, nil
}

// namesPersonExactly reports whether the ladder found someone whose handle,
// real name or display name is the input itself — the person reading of a
// bare word, as opposed to a fragment that merely contains it.
func namesPersonExactly(res provider.PersonResolution, input string) bool {
	if res.Resolved {
		return res.Via != "unique-match"
	}
	for _, c := range res.Candidates {
		if !c.Deleted && (strings.EqualFold(c.Handle, input) ||
			strings.EqualFold(c.RealName, input) || strings.EqualFold(c.DisplayName, input)) {
			return true
		}
	}
	return false
}

// personTarget resolves a person through the ladder and opens their DM when
// the policy accepts the rung. A deactivated person (reads only) is reached
// through a DM that already exists — never by opening one.
func personTarget(ctx context.Context, ap *provider.ApiProvider, input string, policy provider.Policy, param string) (string, *targetError) {
	res := ap.ResolvePersonFor(input, policy)
	if !policy.Accepts(res) {
		if policy == provider.ReadPolicy {
			return "", &targetError{Message: fmt.Sprintf("Could not resolve %q (%s)", input, res.Reason), miss: &res}
		}
		return "", unresolvedPerson(input, res, param)
	}
	if strings.HasPrefix(res.Via, "deactivated") {
		if dm, ok := ap.ExistingDM(res.UserID); ok {
			return dm, nil
		}
		return "", &targetError{
			Message:  fmt.Sprintf("%s (@%s) is deactivated and has no DM history with you.", res.DisplayName, res.Handle),
			Guidance: fmt.Sprintf("Search their traffic instead: messages query='from:@%s'", res.Handle),
		}
	}
	dm, err := ap.OpenDM(ctx, res.UserID)
	if err != nil {
		log.Printf("Failed to open DM for %q: %v", input, err)
		return "", &targetError{
			Message:  fmt.Sprintf("Could not open a conversation with '%s', so nothing was done.", input),
			Guidance: "Retry, or check the handle: estate view='people'",
		}
	}
	return dm, nil
}

// unresolvedPerson explains a person a write may not act on. Deactivated
// people are listed as such but never offered as a retry target.
func unresolvedPerson(input string, res provider.PersonResolution, param string) *targetError {
	if res.Resolved {
		// One person matched, but not by exact handle.
		label := candidateLabel(provider.PersonCandidate{Handle: res.Handle, DisplayName: res.DisplayName})
		return &targetError{
			Message:  fmt.Sprintf("'%s' is not an exact handle, so nothing was done. Did you mean %s?", input, label),
			Guidance: fmt.Sprintf("Writes resolve only an exact @handle (or @me). Retry with %s='@%s'", param, res.Handle),
		}
	}

	var retry string
	for _, c := range res.Candidates {
		if !c.Deleted && c.Handle != "" {
			retry = c.Handle
			break
		}
	}
	if len(res.Candidates) == 0 {
		return &targetError{
			Message:  fmt.Sprintf("Could not find a channel or person named '%s', so nothing was done.", input),
			Guidance: "See available channels: estate view='channels' — or find a person: estate view='people' person='<name>'",
		}
	}

	var b strings.Builder
	if retry == "" {
		fmt.Fprintf(&b, "'%s' matches only deactivated people, who can't be messaged. Nothing was done.", input)
	} else {
		fmt.Fprintf(&b, "'%s' is not an exact handle, so nothing was done. Candidates:", input)
	}
	for _, c := range res.Candidates {
		b.WriteString("\n  " + candidateLabel(c))
		if c.Title != "" {
			fmt.Fprintf(&b, ", %s", c.Title)
		}
		if c.Deleted {
			b.WriteString(" [deactivated]")
		}
	}
	guidance := "Nothing to retry: a deactivated account cannot receive messages."
	if retry != "" {
		guidance = fmt.Sprintf("Retry with the exact @handle of the person you mean, e.g. %s='@%s'", param, retry)
	}
	return &targetError{Message: b.String(), Guidance: guidance}
}

// candidateLabel names a person as '@handle (Display Name)', or by name
// alone when the record has no handle — never a bare '@'.
func candidateLabel(c provider.PersonCandidate) string {
	if c.Handle == "" {
		if c.DisplayName != "" {
			return c.DisplayName
		}
		return c.RealName
	}
	if c.DisplayName != "" && c.DisplayName != c.Handle {
		return fmt.Sprintf("@%s (%s)", c.Handle, c.DisplayName)
	}
	return "@" + c.Handle
}
