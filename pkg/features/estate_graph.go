package features

import (
	"fmt"
	"strings"

	"github.com/aaronsb/slack-mcp/pkg/report"
)

// The graph render seam (ADR-008, amendment 2026-09-30): the same typed
// view data the markdown renderers read, reshaped as nodes and edges. No
// computation the view did not already do, and nothing the markdown does
// not already show — the same page windows, no hour-level data, and
// names only. Node IDs are page-local (n1, n2, ...); Slack IDs serve as
// join keys here and are never emitted. Where a view's label is a
// labelled-unknown fallback carrying a raw ID, the graph says what it is
// instead.

type graphBuilder struct {
	g    report.Graph
	ids  map[string]string // join key -> page-local node ID
	seen map[string]bool   // edge dedupe: source|target|kind
}

func newGraphBuilder(title, subtitle string) *graphBuilder {
	return &graphBuilder{
		g:    report.Graph{Title: title, Subtitle: subtitle},
		ids:  map[string]string{},
		seen: map[string]bool{},
	}
}

// node returns the page-local ID for key, adding the node on first sight.
// A later sighting appends its detail lines to the existing node.
func (b *graphBuilder) node(key, label, kind string, detail ...string) string {
	if id, ok := b.ids[key]; ok {
		for i := range b.g.Nodes {
			if b.g.Nodes[i].ID == id {
				b.g.Nodes[i].Detail = append(b.g.Nodes[i].Detail, detail...)
			}
		}
		return id
	}
	id := fmt.Sprintf("n%d", len(b.g.Nodes)+1)
	b.ids[key] = id
	b.g.Nodes = append(b.g.Nodes, report.Node{ID: id, Label: label, Kind: kind, Detail: detail})
	return id
}

func (b *graphBuilder) edge(from, to, kind string, weight int, label string) {
	k := from + "|" + to + "|" + kind
	if b.seen[k] {
		return
	}
	b.seen[k] = true
	if weight < 1 {
		weight = 1
	}
	b.g.Edges = append(b.g.Edges, report.Edge{Source: from, Target: to, Kind: kind, Weight: weight, Label: label})
}

// convNode adds a conversation by its rendered label, so the same channel
// reached through the footprint, the created list, or a convergence cell
// is one node.
func (b *graphBuilder) convNode(labels map[string]convInfo, conv string, detail ...string) string {
	info, ok := labels[conv]
	switch {
	case !ok:
		// labelFor's fallback is the raw conversation ID.
		return b.node("conv:"+conv, "unnamed conversation", report.KindChannel, detail...)
	case info.IsIM:
		label := info.Label
		if info.Counterpart != "" && label == "DM "+info.Counterpart {
			label = "DM (unresolved user)"
		}
		return b.node("conv:"+conv, label, report.KindDM, detail...)
	default:
		return b.node("chan:"+info.Label, info.Label, report.KindChannel, detail...)
	}
}

// personNode adds a person keyed by user ID, labelled as the view names them.
func (b *graphBuilder) personNode(id, label string, detail ...string) string {
	if label == "external ("+id+")" {
		label = "external user"
	}
	if label == "" {
		label = "unresolved user"
	}
	return b.node("user:"+id, label, report.KindPerson, detail...)
}

const graphCaveat = "Activity is as observed from reads through this server, inside the attention ledger's 90-day window. Edges are observations, not judgments."

// personGraph is the person view's ego network: the seed at the center,
// the surfaces and counterparts of the page the markdown shows, and the
// channels the seed created.
func personGraph(v *personViewData) report.Graph {
	title := "Person — " + v.Label
	if v.Title != "" {
		title += " (" + v.Title + ")"
	}
	b := newGraphBuilder(title, fmt.Sprintf("Ego network, last %d days.", v.Days))
	seed := b.node("user:"+v.ID, v.Label, report.KindSeed, fmt.Sprintf("last %d days", v.Days))

	from, to, _ := pageWindow(len(v.Footprint), v.Offset, 10)
	for _, row := range v.Footprint[from:to] {
		detail := []string{plural(row.Days, "active day")}
		if len(row.Strips) > 0 {
			last := row.Strips[len(row.Strips)-1]
			detail = append(detail, fmt.Sprintf("latest strip %s → %s", last.From, last.To))
		}
		c := b.convNode(v.Labels, row.Conv, detail...)
		b.edge(seed, c, report.EdgeActive, row.Days, plural(row.Days, "active day"))
	}

	cfrom, cto, _ := pageWindow(len(v.Counterparts), v.Offset, 5)
	for _, row := range v.Counterparts[cfrom:cto] {
		label := plural(row.CoDays, "co-active day")
		if row.DMDays > 0 {
			label += " + " + plural(row.DMDays, "DM day")
		}
		p := b.personNode(row.ID, v.Names[row.ID], label)
		b.edge(seed, p, report.EdgeCoActive, row.CoDays+row.DMDays, label)
	}

	for _, name := range v.Created {
		label := "#" + name
		c := b.node("chan:"+label, label, report.KindChannel, "created by "+v.Label)
		b.edge(seed, c, report.EdgeCreated, 1, "created")
	}

	b.g.Notes = []string{graphCaveat}
	if v.WindowEncounters > 0 {
		b.g.Notes = append(b.g.Notes, fmt.Sprintf("Backed by %d observed encounters in the window.", v.WindowEncounters))
	}
	if remaining := len(v.Footprint) - to; remaining > 0 {
		b.g.Notes = append(b.g.Notes, fmt.Sprintf("%d more surfaces beyond this page: render again with offset=%d.", remaining, to))
	}
	return b.g
}

// aboutGraph is the about view's composite: the seed, the families of its
// founder plane, the top of its activity plane and circle, and — after a
// deeper scan — where the circle converges. Caps match the markdown's.
func aboutGraph(v *aboutViewData) report.Graph {
	p := v.Person
	title := "About — " + p.Label
	if p.Title != "" {
		title += " (" + p.Title + ")"
	}
	sub := fmt.Sprintf("Founder plane, activity plane, and circle, last %d days.", p.Days)
	if v.Convergence != nil {
		sub = fmt.Sprintf("Founder plane, activity plane, circle, and where the circle converges, last %d days.", p.Days)
	}
	b := newGraphBuilder(title, sub)
	seed := b.node("user:"+p.ID, p.Label, report.KindSeed, fmt.Sprintf("last %d days", p.Days))

	if v.Families != nil {
		for i, f := range v.Families.Families {
			if i >= 4 {
				break
			}
			n := b.node("family:"+f.Stem, f.Stem, report.KindFamily, plural(len(f.Channels), "channel"))
			b.edge(seed, n, report.EdgeFounder, 1, "founder plane: "+plural(len(f.Channels), "channel"))
		}
	}

	for i, row := range p.Footprint {
		if i >= 5 {
			break
		}
		c := b.convNode(p.Labels, row.Conv, plural(row.Days, "active day"))
		b.edge(seed, c, report.EdgeActive, row.Days, plural(row.Days, "active day"))
	}

	for i, row := range p.Counterparts {
		if i >= 5 {
			break
		}
		days := row.CoDays + row.DMDays
		n := b.personNode(row.ID, p.Names[row.ID], plural(days, "co-active day"))
		b.edge(seed, n, report.EdgeCoActive, days, plural(days, "co-active day"))
	}

	if cv := v.Convergence; cv != nil {
		for i, cell := range cv.Cells {
			if i >= 5 {
				break
			}
			c := b.convNode(cv.Labels, cell.Conv)
			label := "co-active " + plural(cell.CoDays, "day")
			for _, id := range cell.Seen {
				var who string
				if id == p.ID {
					who = seed
				} else {
					who = b.personNode(id, cv.People[id])
				}
				b.edge(who, c, report.EdgeConverges, cell.CoDays, label)
			}
		}
	}

	b.g.Notes = []string{graphCaveat, "Summary view: full lists page through view='person', view='families', view='convergence'."}
	return b.g
}

// ---- render='graph' ----

const renderGraph = "graph"

// viewRender reads and validates the render parameter. A graph is drawn
// for about and person only — the ego-network views; every other view
// refuses rather than silently ignoring the parameter.
func viewRender(view string, params map[string]interface{}) (string, *FeatureResult) {
	raw, has := params["render"]
	if !has || raw == nil || raw == "" {
		return "", nil
	}
	render, ok := raw.(string)
	if !ok || strings.TrimSpace(render) != renderGraph {
		return "", &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("Unknown render %v — the one render is render='graph' (view='about' or view='person').", raw),
		}
	}
	if view != "about" && view != "person" {
		return "", &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("render='graph' draws view='about' and view='person' only, not view='%s'.", view),
			Guidance: "Draw a person's ego network: estate view='person' person='@<handle>' render='graph', or the composite: estate view='about' person='@<handle>' render='graph'.",
		}
	}
	return renderGraph, nil
}

// graphReport is what a render wrote, for the markdown to announce.
type graphReport struct {
	Path, URL    string
	Nodes, Edges int
	Skipped      string // why nothing was drawn (a person miss)
	Err          string
}

// writeGraphReport draws a view's typed data through the seam and writes
// the page. A failure is reported beside the view, never instead of it.
func writeGraphReport(view string, v interface{}) *graphReport {
	var g report.Graph
	var subject string
	switch d := v.(type) {
	case *personViewData:
		if d.Miss != nil {
			return &graphReport{Skipped: "the person did not resolve, so there is nothing to draw"}
		}
		g, subject = personGraph(d), d.Handle
	case *aboutViewData:
		if d.Person == nil || d.Person.Miss != nil {
			return &graphReport{Skipped: "the person did not resolve, so there is nothing to draw"}
		}
		g, subject = aboutGraph(d), d.Person.Handle
	default:
		return &graphReport{Skipped: "this view has no graph"}
	}
	page, err := report.Page(g)
	if err != nil {
		return &graphReport{Err: err.Error()}
	}
	path, err := report.Write(view, subject, page)
	if err != nil {
		return &graphReport{Err: err.Error()}
	}
	return &graphReport{Path: path, URL: report.FileURL(path), Nodes: len(g.Nodes), Edges: len(g.Edges)}
}

func formatGraphReport(r *graphReport) string {
	switch {
	case r.Skipped != "":
		return "**Graph report (render='graph'):** not written — " + r.Skipped + "."
	case r.Err != "":
		return "**Graph report (render='graph'):** not written — " + r.Err + ". The view above is unaffected."
	}
	return fmt.Sprintf("**Graph report (render='graph'):** %d nodes, %d edges written to `%s`\n"+
		"Open: %s\n"+
		"A static, self-contained page on this server's host: no network access, nothing served or launched. "+
		"It holds relationship data (file mode 0600) and is replaced by the next render of this view and person.",
		r.Nodes, r.Edges, r.Path, r.URL)
}
