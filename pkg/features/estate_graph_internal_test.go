package features

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/estate"
	"github.com/aaronsb/slack-mcp/pkg/report"
)

// slackID matches the shape of Slack's internal identifiers (U0AAA,
// C0ENG...): the no-ID rule says none may reach a rendered surface.
var slackID = regexp.MustCompile(`\b[UCDGWT][A-Z0-9]{2,}\b`)

func graphNode(t *testing.T, g report.Graph, label string) report.Node {
	t.Helper()
	for _, n := range g.Nodes {
		if n.Label == label {
			return n
		}
	}
	t.Fatalf("no node labelled %q in %+v", label, g.Nodes)
	return report.Node{}
}

func graphEdge(t *testing.T, g report.Graph, from, to string) report.Edge {
	t.Helper()
	a, b := graphNode(t, g, from).ID, graphNode(t, g, to).ID
	for _, e := range g.Edges {
		if (e.Source == a && e.Target == b) || (e.Source == b && e.Target == a) {
			return e
		}
	}
	t.Fatalf("no edge %q — %q in %+v", from, to, g.Edges)
	return report.Edge{}
}

func assertNoSlackIDs(t *testing.T, g report.Graph) {
	t.Helper()
	b, _ := json.Marshal(g)
	if m := slackID.FindString(string(b)); m != "" {
		t.Fatalf("graph carries Slack ID %q: %s", m, b)
	}
}

func samplePerson() *personViewData {
	return &personViewData{
		ID: "U0SEED", Label: "Sarah Chen", Title: "Engineer", Handle: "schen", Days: 30,
		Footprint: []footprintRow{
			{Conv: "C0ENG", Days: 5, Strips: []estate.Strip{{From: "2026-09-20", To: "2026-09-24"}}},
			{Conv: "D0DM", Days: 2},
			{Conv: "C0RAW", Days: 1}, // no label known: the fallback is the raw ID
		},
		Counterparts: []counterpartRow{
			{ID: "U0AAA", CoDays: 4, DMDays: 2},
			{ID: "U0EXT", CoDays: 1},
		},
		Created:     []string{"eng", "acme-sales"},
		Parallelism: map[int]int{1: 10, 2: 3}, // operator hours: never graphed
		Peak:        2,
		Labels: map[string]convInfo{
			"C0ENG": {Label: "#eng"},
			"D0DM":  {Label: "DM U0AAA", IsIM: true, Counterpart: "U0AAA"},
		},
		Names: map[string]string{
			"U0AAA": "Aaron Bockelie",
			"U0EXT": "external (U0EXT)",
		},
	}
}

func TestPersonGraphIsTheEgoNetwork(t *testing.T) {
	g := personGraph(samplePerson())

	seed := graphNode(t, g, "Sarah Chen")
	if seed.Kind != report.KindSeed {
		t.Fatalf("seed kind %q", seed.Kind)
	}
	if e := graphEdge(t, g, "Sarah Chen", "#eng"); e.Weight != 5 || e.Kind != report.EdgeActive {
		t.Fatalf("footprint edge %+v, want active weight 5", e)
	}
	if e := graphEdge(t, g, "Sarah Chen", "Aaron Bockelie"); e.Weight != 6 || e.Kind != report.EdgeCoActive {
		t.Fatalf("counterpart edge %+v, want co-active weight 6 (4 co + 2 DM)", e)
	}
	// A created channel already in the footprint is one node with two edges.
	eng := 0
	for _, n := range g.Nodes {
		if n.Label == "#eng" {
			eng++
		}
	}
	if eng != 1 {
		t.Fatalf("#eng appears as %d nodes", eng)
	}
	if e := graphEdge(t, g, "Sarah Chen", "#acme-sales"); e.Kind != report.EdgeCreated {
		t.Fatalf("created edge %+v", e)
	}
	if !strings.Contains(strings.Join(graphNode(t, g, "#eng").Detail, "|"), "latest strip 2026-09-20 → 2026-09-24") {
		t.Fatalf("strip detail missing: %+v", graphNode(t, g, "#eng"))
	}
}

func TestConversationsSharingALabelKeepTheirOwnEdges(t *testing.T) {
	v := samplePerson()
	v.Footprint = []footprintRow{{Conv: "C0ENG", Days: 5}, {Conv: "C0ENG2", Days: 2}}
	v.Labels["C0ENG2"] = convInfo{Label: "#eng"}
	v.Counterparts = nil

	g := personGraph(v)
	var weights []int
	engIDs := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Label == "#eng" {
			engIDs[n.ID] = true
		}
	}
	for _, e := range g.Edges {
		if engIDs[e.Target] && e.Kind == report.EdgeActive {
			weights = append(weights, e.Weight)
		}
	}
	if len(engIDs) != 2 || len(weights) != 2 || weights[0]+weights[1] != 7 {
		t.Fatalf("two #eng conversations collapsed: nodes %v, active weights %v", engIDs, weights)
	}
	// The created list (names only) joins the first #eng, not a third node.
	created := 0
	for _, e := range g.Edges {
		if e.Kind == report.EdgeCreated && engIDs[e.Target] {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created #eng joined %d drawn conversations, want 1", created)
	}
}

func TestPersonGraphNeverCarriesIDsOrHours(t *testing.T) {
	g := personGraph(samplePerson())
	assertNoSlackIDs(t, g)

	graphNode(t, g, "unnamed conversation")
	graphNode(t, g, "DM (unresolved user)")
	graphNode(t, g, "external user")

	b, _ := json.Marshal(g)
	for _, hourish := range []string{"parallel", "concurrent", "hour"} {
		if strings.Contains(strings.ToLower(string(b)), hourish) {
			t.Fatalf("graph carries hour-level data (%q): %s", hourish, b)
		}
	}
	for _, n := range g.Nodes {
		if !regexp.MustCompile(`^n\d+$`).MatchString(n.ID) {
			t.Fatalf("node ID %q is not page-local", n.ID)
		}
	}
}

func TestPersonGraphShowsThePageTheMarkdownShows(t *testing.T) {
	v := samplePerson()
	v.Footprint = nil
	v.Counterparts = nil
	for i := 0; i < 15; i++ {
		v.Footprint = append(v.Footprint, footprintRow{Conv: "C" + string(rune('a'+i)), Days: 20 - i})
		v.Labels["C"+string(rune('a'+i))] = convInfo{Label: "#ch" + string(rune('a'+i))}
	}
	for i := 0; i < 8; i++ {
		id := "P" + string(rune('a'+i))
		v.Counterparts = append(v.Counterparts, counterpartRow{ID: id, CoDays: 10 - i})
		v.Names[id] = "Person " + string(rune('A'+i))
	}
	v.Created = nil

	g := personGraph(v)
	channels, people := 0, 0
	for _, n := range g.Nodes {
		switch n.Kind {
		case report.KindChannel:
			channels++
		case report.KindPerson:
			people++
		}
	}
	if channels != 10 || people != 5 {
		t.Fatalf("graph shows %d surfaces and %d counterparts, markdown shows 10 and 5", channels, people)
	}

	v.Offset = 10
	g = personGraph(v)
	graphNode(t, g, "#chk") // surface 11 is on the second page
	for _, n := range g.Nodes {
		if n.Label == "#cha" {
			t.Fatal("first-page surface on the second page")
		}
	}
}

func TestAboutGraphComposesFamiliesAndConvergence(t *testing.T) {
	v := &aboutViewData{
		Person: samplePerson(),
		Families: &estateFamiliesData{Families: []famGroup{
			{Stem: "acme", Channels: []famChannel{{Name: "acme-sales"}, {Name: "acme-impl"}}},
		}},
		Convergence: &convergenceViewData{
			People: map[string]string{"U0SEED": "Sarah Chen", "U0AAA": "Aaron Bockelie"},
			Cells:  []convergenceCell{{Conv: "C0ENG", CoDays: 3, First: "2026-09-21", Last: "2026-09-23", Seen: []string{"U0AAA", "U0SEED"}}},
			Labels: map[string]convInfo{"C0ENG": {Label: "#eng"}},
		},
		Plan: []readingStep{{Why: "#eng — their densest observed surface", Next: "messages target='#eng'"}},
	}
	g := aboutGraph(v)

	if f := graphNode(t, g, "acme"); f.Kind != report.KindFamily {
		t.Fatalf("family node %+v", f)
	}
	if e := graphEdge(t, g, "Sarah Chen", "acme"); e.Kind != report.EdgeFounder {
		t.Fatalf("founder edge %+v", e)
	}
	// The convergence cell reuses the footprint's #eng and the circle's
	// Aaron node rather than duplicating them.
	if e := graphEdge(t, g, "Aaron Bockelie", "#eng"); e.Kind != report.EdgeConverges || e.Weight != 3 {
		t.Fatalf("convergence edge %+v", e)
	}
	seen := map[string]int{}
	for _, n := range g.Nodes {
		seen[n.Label]++
	}
	for label, n := range seen {
		if n > 1 {
			t.Fatalf("%q appears as %d nodes", label, n)
		}
	}
	// About shows what its markdown shows: no created-channel list.
	for _, n := range g.Nodes {
		if n.Label == "#acme-sales" {
			t.Fatal("about graph carries the person view's created list")
		}
	}
	// The reading plan is prose built around handles; it stays in the
	// markdown the same call returns.
	if strings.Contains(strings.Join(g.Notes, "\n"), "densest observed surface") {
		t.Fatalf("reading plan leaked into the graph notes: %v", g.Notes)
	}
	assertNoSlackIDs(t, g)
}
