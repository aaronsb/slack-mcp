package provider

// ADR-005's resolution ladder: a person named in a parameter resolves from
// cached state, never from the caller guessing a handle format. The ladder
// runs entirely against the users map and the estate fold — no network —
// and a miss is a ranked candidate set with evidence, not an error, so the
// caller never needs a second lookup to know what to try instead.

import (
	"sort"
	"strings"

	"github.com/slack-go/slack"
)

// PersonCandidate is one near-match, carrying enough evidence — title,
// tombstone dates — that choosing needs no further call.
type PersonCandidate struct {
	ID          string   `json:"id"`
	Handle      string   `json:"handle"`
	DisplayName string   `json:"displayName"`
	RealName    string   `json:"realName,omitempty"`
	Title       string   `json:"title,omitempty"`
	Deleted     bool     `json:"deleted,omitempty"`
	GoneReason  string   `json:"goneReason,omitempty"`
	GoneBetween []string `json:"goneBetween,omitempty"`
}

// PersonResolution is the ladder's answer for one input.
type PersonResolution struct {
	Input    string
	Resolved bool
	// Handle, UserID, DisplayName and Via are set when Resolved.
	Handle      string
	UserID      string
	DisplayName string
	Via         string // "self" | "user-id" | "exact-handle" | "unique-name" | "unique-match"
	// Reason is set when not Resolved: "empty", "ambiguous", "tombstoned",
	// "never_seen", or "unswept". Candidates carry the near-matches for
	// ambiguous and tombstoned outcomes.
	Reason     string
	Candidates []PersonCandidate
}

const maxCandidates = 5

// ResolvePerson runs the ladder for one person-shaped input. Reads may act
// on a resolved answer directly; writes auto-resolve only when Via is
// "user-id" or "exact-handle" (ADR-005: a wrong search costs a retry, a
// wrong send messages a stranger).
func (ap *ApiProvider) ResolvePerson(input string) PersonResolution {
	res := PersonResolution{Input: input}
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "@"))
	if name == "" {
		res.Reason = "empty"
		return res
	}

	users := ap.ProvideUsersMap()

	lower := strings.ToLower(name)

	// The operator's own identity resolves by name: 'me' answers the
	// whoami question through the same ladder everything else uses.
	if lower == "me" || lower == "self" || lower == "myself" {
		if ap.selfUserID != "" {
			if u, ok := users[ap.selfUserID]; ok {
				return resolvedFrom(res, u, "self")
			}
		}
		res.Reason = "unswept"
		return res
	}

	if looksLikeUserID(name) {
		if u, ok := users[name]; ok && !u.Deleted {
			return resolvedFrom(res, u, "user-id")
		}
		return ap.missOutcome(res, lower)
	}

	// Step 1: exact handle. Slack guarantees uniqueness, so this never
	// produces candidates.
	for _, u := range users {
		if !u.Deleted && strings.EqualFold(u.Name, name) {
			return resolvedFrom(res, u, "exact-handle")
		}
	}

	// Step 2: exact display or real name, unique among the living.
	var exact []slack.User
	for _, u := range users {
		if u.Deleted {
			continue
		}
		if strings.EqualFold(u.RealName, name) || strings.EqualFold(u.Profile.DisplayName, name) {
			exact = append(exact, u)
		}
	}
	if len(exact) == 1 {
		return resolvedFrom(res, exact[0], "unique-name")
	}
	if len(exact) > 1 {
		res.Reason = "ambiguous"
		res.Candidates = rankCandidates(exact, lower)
		return res
	}

	// Step 3: substring across handle, display and real name. A single
	// living match resolves — that is the read path's zero-hop answer; the
	// caller says how it resolved.
	var loose []slack.User
	for _, u := range users {
		if u.Deleted {
			continue
		}
		if strings.Contains(strings.ToLower(u.Name), lower) ||
			strings.Contains(strings.ToLower(u.RealName), lower) ||
			strings.Contains(strings.ToLower(u.Profile.DisplayName), lower) {
			loose = append(loose, u)
		}
	}
	if len(loose) == 1 {
		return resolvedFrom(res, loose[0], "unique-match")
	}
	if len(loose) > 1 {
		res.Reason = "ambiguous"
		res.Candidates = rankCandidates(loose, lower)
		return res
	}

	return ap.missOutcome(res, lower)
}

// missOutcome distinguishes the three claims an empty ladder can honestly
// make: the person existed and left (dated candidates), the estate was
// completely enumerated and never held them, or no full sweep has happened
// and absence cannot be asserted.
func (ap *ApiProvider) missOutcome(res PersonResolution, lower string) PersonResolution {
	var gone []PersonCandidate
	for id, rec := range ap.EstateUsers() {
		if rec.Gone == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(rec.Props.Name), lower) &&
			!strings.Contains(strings.ToLower(rec.Props.RealName), lower) &&
			!strings.Contains(strings.ToLower(rec.Props.DisplayName), lower) &&
			id != lower && !strings.EqualFold(id, res.Input) {
			continue
		}
		display := rec.Props.RealName
		if display == "" {
			display = rec.Props.Name
		}
		from := rec.Gone.NotBefore
		if from.IsZero() {
			from = rec.Gone.At
		}
		gone = append(gone, PersonCandidate{
			ID: id, Handle: rec.Props.Name, DisplayName: display,
			RealName: rec.Props.RealName, Title: rec.Props.Title,
			Deleted: true, GoneReason: rec.Gone.Reason,
			GoneBetween: []string{from.Format("2006-01-02"), rec.Gone.At.Format("2006-01-02")},
		})
	}
	if len(gone) > 0 {
		sort.Slice(gone, func(i, j int) bool { return gone[i].Handle < gone[j].Handle })
		if len(gone) > maxCandidates {
			gone = gone[:maxCandidates]
		}
		res.Reason = "tombstoned"
		res.Candidates = gone
		return res
	}
	if ap.EstateLastFullSweep().IsZero() {
		res.Reason = "unswept"
		return res
	}
	res.Reason = "never_seen"
	return res
}

func resolvedFrom(res PersonResolution, u slack.User, via string) PersonResolution {
	res.Resolved = true
	res.Handle = u.Name
	res.UserID = u.ID
	res.Via = via
	res.DisplayName = u.RealName
	if res.DisplayName == "" {
		res.DisplayName = u.Name
	}
	return res
}

// rankCandidates orders near-matches: prefix hits before substring hits,
// then by handle, capped so an ambiguous answer stays choosable.
func rankCandidates(users []slack.User, lower string) []PersonCandidate {
	prefix := func(u slack.User) bool {
		return strings.HasPrefix(strings.ToLower(u.Name), lower) ||
			strings.HasPrefix(strings.ToLower(u.RealName), lower) ||
			strings.HasPrefix(strings.ToLower(u.Profile.DisplayName), lower)
	}
	sort.Slice(users, func(i, j int) bool {
		pi, pj := prefix(users[i]), prefix(users[j])
		if pi != pj {
			return pi
		}
		return users[i].Name < users[j].Name
	})
	if len(users) > maxCandidates {
		users = users[:maxCandidates]
	}
	out := make([]PersonCandidate, 0, len(users))
	for _, u := range users {
		display := u.RealName
		if display == "" {
			display = u.Name
		}
		out = append(out, PersonCandidate{
			ID: u.ID, Handle: u.Name, DisplayName: display,
			RealName: u.RealName, Title: u.Profile.Title,
		})
	}
	return out
}

// looksLikeUserID reports whether s has the shape of a Slack user ID: real
// IDs are at least nine characters and always carry digits. Without the
// bounds, an all-caps name like URSULA would be misrouted as an ID and skip
// every ladder rung.
func looksLikeUserID(s string) bool {
	if len(s) < 9 || (s[0] != 'U' && s[0] != 'W') {
		return false
	}
	digits := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits = true
			continue
		}
		if !(c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return digits
}
