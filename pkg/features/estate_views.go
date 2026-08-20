package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// EstateViews is ADR-008's read surface: named views that answer whole
// relationship questions from the fold, so the agent never does set math
// over listings. Stage 1 ships the families view; initiatives, person,
// convergence, and the about entry point follow the attention ledger.
var EstateViews = &Feature{
	Name:        "estate",
	Description: "Relationship views over the accumulated estate. view='families' groups engagement channels by name stem — lifecycle span, phases, creators — answered from local state at zero Slack calls. Narrow with search='stem' or anchor on a person with person='@handle'.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"view": map[string]interface{}{
				"type":        "string",
				"description": "Which view to execute. Currently: 'families'",
			},
			"search": map[string]interface{}{
				"type":        "string",
				"description": "Filter families by stem or member channel name (partial match). Relaxes the default signal criteria to any matching group.",
			},
			"person": map[string]interface{}{
				"type":        "string",
				"description": "Anchor on a person (@handle, name, or user ID): only groups containing channels they created, including single-channel groups — surfaces cross-stem work that stem grouping alone separates.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Maximum families to return (default 10, max 50)",
				"default":     10,
			},
		},
		"required": []string{"view"},
	},
	Handler: estateViewsHandler,
}

// famPhaseTokens marks the engagement-phase suffixes the families motif
// recognizes; ported from the ADR-008 evidence probe.
var famPhaseTokens = map[string]bool{
	"sales": true, "assessment": true, "implementation": true,
	"training": true, "support": true, "delivery": true,
	"migration": true, "onboarding": true, "discovery": true,
}

type famChannel struct {
	Name     string
	Creator  string
	Created  int64
	Archived bool
	// GoneFrom/GoneTo bound the observation interval of an absence
	// tombstone; zero for live channels.
	GoneFrom time.Time
	GoneTo   time.Time
}

type famGroup struct {
	Stem     string
	Channels []famChannel
	From, To int64
	Phased   int
}

type estateFamiliesData struct {
	Families      []famGroup
	TotalFamilies int
	Considered    int
	GoneIncluded  int
	FoldOwned     int
	Coverage      provider.EstateCoverageInfo
	CreatorNames  map[string]string
	Search        string
	PersonLabel   string
	PersonMiss    *provider.PersonResolution
	Shown         int
}

func estateViewsHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	apiProvider, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available"}, nil
	}

	view, _ := params["view"].(string)
	if view != "families" {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("Unknown view %q. Available views: families", view),
			Guidance: "The families view groups engagement channels by name stem. More views (initiatives, person, convergence, about) arrive with the attention ledger.",
		}, nil
	}

	search := ""
	if s, ok := params["search"].(string); ok {
		search = strings.ToLower(strings.TrimSpace(s))
	}
	person, _ := params["person"].(string)
	person = strings.TrimSpace(person)

	limit := 10
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
		if limit > 50 {
			limit = 50
		}
		if limit < 1 {
			limit = 1
		}
	}

	data := &estateFamiliesData{Search: search, CreatorNames: map[string]string{}}
	data.Coverage = apiProvider.EstateCoverage()

	var personID string
	if person != "" {
		res := apiProvider.ResolvePerson(person)
		if !res.Resolved {
			data.PersonMiss = &res
			return &FeatureResult{
				Success: true,
				Data:    map[string]interface{}{"view": data},
				Message: fmt.Sprintf("Could not resolve %q (%s)", person, res.Reason),
			}, nil
		}
		personID = res.UserID
		data.PersonLabel = res.DisplayName
		if data.PersonLabel == "" {
			data.PersonLabel = res.Handle
		}
	}

	// Merge the two sources, keyed by conversation ID so a reused name
	// never grafts a dead channel's ownership onto a live one — the two
	// render side by side in their stem group instead. The snapshot
	// carries ownership today; the fold carries it from the next sweep
	// on, and is the only source that still knows the channels that are
	// gone.
	channels := map[string]famChannel{}
	for _, ch := range apiProvider.GetCachedChannels() {
		if ch.IsIM || ch.IsMpIM || ch.Name == "" {
			continue
		}
		channels[ch.ID] = famChannel{
			Name:     ch.Name,
			Creator:  ch.Creator,
			Created:  int64(ch.Created),
			Archived: ch.IsArchived,
		}
	}
	for id, rec := range apiProvider.EstateChannels() {
		p := rec.Props
		if p.IsIM || p.IsMpim || p.Name == "" {
			continue
		}
		if p.Creator != "" {
			data.FoldOwned++
		}
		if existing, ok := channels[id]; ok {
			if existing.Creator == "" && p.Creator != "" {
				existing.Creator = p.Creator
				existing.Created = p.Created
				channels[id] = existing
			}
			continue
		}
		if rec.Gone == nil {
			continue
		}
		data.GoneIncluded++
		channels[id] = famChannel{
			Name:     p.Name,
			Creator:  p.Creator,
			Created:  p.Created,
			Archived: p.IsArchived,
			GoneFrom: rec.Gone.NotBefore,
			GoneTo:   rec.Gone.At,
		}
	}
	data.Considered = len(channels)

	groups := map[string][]famChannel{}
	for _, ch := range channels {
		stem := strings.SplitN(ch.Name, "-", 2)[0]
		groups[stem] = append(groups[stem], ch)
	}

	relaxed := search != "" || personID != ""
	var fams []famGroup
	for stem, chs := range groups {
		if search != "" {
			match := strings.Contains(stem, search)
			for _, c := range chs {
				if strings.Contains(strings.ToLower(c.Name), search) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if personID != "" {
			created := false
			for _, c := range chs {
				if c.Creator == personID {
					created = true
				}
			}
			if !created {
				continue
			}
		}
		f := famGroup{Stem: stem, Channels: chs, From: 1 << 62}
		for _, c := range chs {
			toks := strings.Split(c.Name, "-")
			if famPhaseTokens[toks[len(toks)-1]] {
				f.Phased++
			}
			if c.Created > 0 && c.Created < f.From {
				f.From = c.Created
			}
			if c.Created > f.To {
				f.To = c.Created
			}
		}
		if !relaxed && (len(chs) < 2 || f.Phased == 0) {
			continue
		}
		sort.Slice(f.Channels, func(i, j int) bool {
			if f.Channels[i].Created != f.Channels[j].Created {
				return f.Channels[i].Created < f.Channels[j].Created
			}
			return f.Channels[i].Name < f.Channels[j].Name
		})
		fams = append(fams, f)
	}
	sort.Slice(fams, func(i, j int) bool {
		if len(fams[i].Channels) != len(fams[j].Channels) {
			return len(fams[i].Channels) > len(fams[j].Channels)
		}
		return fams[i].Stem < fams[j].Stem
	})

	data.TotalFamilies = len(fams)
	if len(fams) > limit {
		fams = fams[:limit]
	}
	data.Families = fams
	data.Shown = len(fams)

	// Resolve creator names once, estate fallback included so a departed
	// creator renders as a dated fact instead of a raw ID.
	users := apiProvider.ProvideUsersMap()
	for _, f := range fams {
		for _, c := range f.Channels {
			if c.Creator == "" {
				continue
			}
			if _, done := data.CreatorNames[c.Creator]; done {
				continue
			}
			if u, ok := users[c.Creator]; ok {
				name := u.RealName
				if name == "" {
					name = u.Name
				}
				if u.Deleted {
					name += " (departed)"
				}
				data.CreatorNames[c.Creator] = name
				continue
			}
			if rec, ok := apiProvider.EstateUser(c.Creator); ok {
				name := rec.Props.RealName
				if name == "" {
					name = rec.Props.Name
				}
				if rec.Gone != nil {
					name += " (departed)"
				}
				data.CreatorNames[c.Creator] = name
				continue
			}
			data.CreatorNames[c.Creator] = c.Creator
		}
	}

	return &FeatureResult{
		Success:     true,
		Data:        map[string]interface{}{"view": data},
		ResultCount: data.Shown,
		Message:     fmt.Sprintf("families view: %d of %d families", data.Shown, data.TotalFamilies),
	}, nil
}

// formatEstate renders the estate views. The rendered markdown is the
// agent's entire world — result.Data never reaches the client.
func formatEstate(result *FeatureResult) string {
	var b strings.Builder

	dataMap, _ := result.Data.(map[string]interface{})
	view, _ := dataMap["view"].(*estateFamiliesData)
	if view == nil {
		b.WriteString(result.Message)
		if result.Guidance != "" {
			b.WriteString("\n\n" + result.Guidance)
		}
		return b.String()
	}

	if view.PersonMiss != nil {
		res := view.PersonMiss
		fmt.Fprintf(&b, "## Estate — families view\n\n%s.\n", result.Message)
		if len(res.Candidates) > 0 {
			b.WriteString("\nClosest matches:\n")
			for _, c := range res.Candidates {
				line := fmt.Sprintf("- @%s — %s", c.Handle, c.DisplayName)
				if c.Title != "" {
					line += " (" + c.Title + ")"
				}
				if c.GoneReason != "" {
					line += fmt.Sprintf(" [gone: %s %s]", c.GoneReason, strings.Join(c.GoneBetween, "…"))
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("\n**Next:** re-query with a handle: estate view='families' person='@<handle>'")
		} else {
			b.WriteString("\n**Next:** search the directory: list-users query='<name>'")
		}
		return b.String()
	}

	title := "## Estate — families view"
	if view.PersonLabel != "" {
		title += fmt.Sprintf(" — created by %s", view.PersonLabel)
	}
	if view.Search != "" {
		title += fmt.Sprintf(" — matching %q", view.Search)
	}
	b.WriteString(title + "\n\n")

	criteria := "≥2 channels, ≥1 phase-tagged"
	if view.Search != "" || view.PersonLabel != "" {
		criteria = "any matching group"
	}
	fmt.Fprintf(&b, "%d families (%s) from %d named channels", view.TotalFamilies, criteria, view.Considered)
	if view.GoneIncluded > 0 {
		fmt.Fprintf(&b, ", %d of them gone channels only the estate still remembers", view.GoneIncluded)
	}
	if view.Shown < view.TotalFamilies {
		fmt.Fprintf(&b, "; showing %d — narrow with search='stem' or raise limit", view.Shown)
	}
	b.WriteString(".\n\n")

	for _, f := range view.Families {
		span := ""
		if f.From < 1<<62 && f.From > 0 {
			span = fmt.Sprintf(", opened %s → %s",
				time.Unix(f.From, 0).Format("2006-01"), time.Unix(f.To, 0).Format("2006-01"))
		}
		fmt.Fprintf(&b, "### %s — %d channels%s\n", f.Stem, len(f.Channels), span)
		for _, c := range f.Channels {
			line := "- #" + c.Name
			if c.Created > 0 {
				line += "  created " + time.Unix(c.Created, 0).Format("2006-01-02")
			}
			if c.Creator != "" {
				line += " by " + view.CreatorNames[c.Creator]
			}
			switch {
			case !c.GoneTo.IsZero():
				line += fmt.Sprintf("  [gone %s…%s]",
					c.GoneFrom.Format("2006-01-02"), c.GoneTo.Format("2006-01-02"))
			case c.Archived:
				line += "  [archived]"
			default:
				line += "  [live]"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	cov := "Coverage: fold executor, 0 Slack calls"
	if view.Coverage.Available {
		cov += fmt.Sprintf("; estate holds %d channels", view.Coverage.Channels)
		if !view.Coverage.LastFullSweep.IsZero() {
			cov += fmt.Sprintf(", last full sweep %s", view.Coverage.LastFullSweep.Format("2006-01-02 15:04"))
		}
	} else {
		cov += "; estate unavailable — served from the session snapshot alone"
	}
	if view.FoldOwned == 0 {
		cov += ". Ownership read from the live snapshot; the estate records it from the next sweep on"
	}
	b.WriteString(cov + ".\n")
	b.WriteString("**Next:** read a family's channel: catch-up channel='#<name>' | narrow: estate view='families' search='<stem>' | person-anchored: estate view='families' person='@<handle>'")

	return b.String()
}
