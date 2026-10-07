package features

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/handle"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// A channel the cache does not hold is looked up once in Slack's own
// channel search before the caller is told it is unknown (ADR-015). Only a
// unique exact name resolves; any other hits come back as candidates, named
// by '#name' and never by ID, for reads and writes alike.

// maxNamedHits caps how many switcher hits a miss names inline.
const maxNamedHits = 8

// channelShapedRE is a Slack channel name: lowercase letters, digits,
// hyphens, and underscores.
var channelShapedRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// channelShaped reports whether a description is one channel-shaped word,
// '#' allowed, and returns it bare. Only such a word reaches the switcher
// from the read path.
func channelShaped(description string) (string, bool) {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(description), "#"))
	return name, channelShapedRE.MatchString(name)
}

// remoteChannelDestination is a '#name' miss: the switcher resolves a
// unique exact name, and anything else is a targetError naming what it
// found. No content has been sent; for a write, nothing was done.
func remoteChannelDestination(ctx context.Context, ap *provider.ApiProvider, input, name, param string) (*resolvedDestination, *targetError) {
	look := ap.LookupChannelRemote(ctx, name)
	if look.Channel != nil {
		return channelDestination(ap, input, *look.Channel), nil
	}
	guidance := fmt.Sprintf("See available channels: estate view='channels' — for a person, use %s='@handle'", param)
	switch {
	case look.Unavailable != "":
		return nil, &targetError{
			Message:  fmt.Sprintf("No channel named '#%s' is known, so nothing was done. %s", name, look.Unavailable),
			Guidance: guidance,
		}
	case len(look.Hits) > 0:
		return nil, &targetError{
			Message:  fmt.Sprintf("%s, so nothing was done. Slack's channel search found %s.", noExactName(look, "'#"+name+"'"), namedHits(look, name)),
			Guidance: fmt.Sprintf("Name one of them exactly: %s='#name'", param),
		}
	}
	return nil, &targetError{
		Message:  fmt.Sprintf("No channel named '#%s' is visible to you: Slack's channel search found none, so nothing was done.", name),
		Guidance: guidance,
	}
}

// noExactName says no hit is named exactly so, and, when Slack had more
// hits than one page, that only the top page was checked.
func noExactName(look provider.ChannelLookup, quoted string) string {
	if look.Total > len(look.Hits) {
		return fmt.Sprintf("No channel among the top %d of %d search hits is named exactly %s", len(look.Hits), look.Total, quoted)
	}
	return fmt.Sprintf("No channel is named exactly %s", quoted)
}

// namedHits renders switcher hits inline: '#name', with a note when the
// hit matched on its purpose or is archived, and a count of the rest.
func namedHits(look provider.ChannelLookup, name string) string {
	shown := look.Hits
	if len(shown) > maxNamedHits {
		shown = shown[:maxNamedHits]
	}
	parts := make([]string, 0, len(shown))
	for _, h := range shown {
		parts = append(parts, "#"+h.Name+hitNote(h, name))
	}
	s := strings.Join(parts, ", ")
	if more := look.Total - len(shown); more > 0 {
		s += fmt.Sprintf(", and %d more", more)
	}
	return s
}

// hitNote says why a hit is a weak match: found in its purpose rather than
// its name, or archived.
func hitNote(h provider.SwitcherChannel, name string) string {
	var notes []string
	if m := matchedOn(h, name); m != "" {
		notes = append(notes, "matched its "+m)
	}
	if h.IsArchived {
		notes = append(notes, "archived")
	}
	if len(notes) == 0 {
		return ""
	}
	return " (" + strings.Join(notes, ", ") + ")"
}

// matchedOn names what a hit matched when its name does not contain the
// query: its purpose when the purpose does, else empty, since Slack's
// match reason is not reported.
func matchedOn(h provider.SwitcherChannel, name string) string {
	q := strings.ToLower(name)
	if strings.Contains(strings.ToLower(h.Name), q) {
		return ""
	}
	if strings.Contains(strings.ToLower(h.Purpose.Value), q) {
		return "purpose"
	}
	return ""
}

// switcherCandidates is a read-path miss's hits in the candidate shape
// resolveAndRead returns for local matches.
func switcherCandidates(look provider.ChannelLookup, name string) []map[string]interface{} {
	hits := look.Hits
	if len(hits) > maxCandidates {
		hits = hits[:maxCandidates]
	}
	options := make([]map[string]interface{}, 0, len(hits))
	for _, h := range hits {
		option := map[string]interface{}{
			"handle": handle.Conversation(h.ID),
			"where":  "#" + h.Name,
			"kind":   "channel",
		}
		if m := matchedOn(h, name); m != "" {
			option["matchedOn"] = m + ", not the name"
		}
		if h.IsArchived {
			option["archived"] = true
		}
		if !h.IsMember {
			option["member"] = false
		}
		options = append(options, option)
	}
	return options
}
