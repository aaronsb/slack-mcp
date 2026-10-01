package features_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/aaronsb/slack-mcp/pkg/features"
	"github.com/aaronsb/slack-mcp/pkg/provider"
	"github.com/aaronsb/slack-mcp/pkg/report"
	"github.com/aaronsb/slack-mcp/pkg/slacktest"
	"github.com/slack-go/slack"
)

// render='graph' (ADR-008 amendment, 2026-09-30): the about and person
// views also write a static graph page under <data dir>/reports.

const hostileName = "</script><script>alert(1)</script>"

// Realistic-shape IDs, so the no-ID assertions have something to catch.
const (
	idSarah = "U0SARAH0001"
	idExt   = "U0EXTERN001"
	idEng   = "C0ENGINE001"
	idEvil  = "C0HOSTILE01"
	idGhost = "C0NONAME001"
)

var fixtureIDs = []string{"U1", idSarah, idExt, idEng, idEvil, idGhost}

func graphFixture(t *testing.T) *provider.ApiProvider {
	t.Helper()
	srv := slacktest.New(t)
	srv.SeedUsers(
		slack.User{ID: "U1", Name: "bockeliea", RealName: "Aaron Bockelie"},
		slack.User{ID: idSarah, Name: "schen", RealName: "Sarah " + hostileName},
	)
	eng := channelWithCreator(idEng, "eng", idSarah)
	evil := channelWithCreator(idEvil, hostileName, idSarah)
	srv.SeedChannels(eng, evil)
	ap := bootedProvider(t, srv)

	for _, d := range []int{1, 2, 3} {
		seedActivity(ap, idEng, idSarah, d)
		seedActivity(ap, idEng, "U1", d)
	}
	seedActivity(ap, idEvil, idSarah, 1)
	seedActivity(ap, idEvil, idExt, 1) // a Slack Connect external: in neither users map nor estate
	seedActivity(ap, idGhost, idSarah, 2)
	return ap
}

func renderEstate(t *testing.T, ap *provider.ApiProvider, params map[string]any) (*features.FeatureResult, string) {
	t.Helper()
	params["_provider"] = ap
	res, err := features.EstateViews.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("estate: %v", err)
	}
	return res, features.FormatResult("estate", res)
}

func dataBlock(t *testing.T, page string) string {
	t.Helper()
	open := `<script type="application/json" id="data">`
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatal("page has no data block")
	}
	rest := page[i+len(open):]
	return rest[:strings.Index(rest, "</script>")]
}

func readReport(t *testing.T, out string) (string, string) {
	t.Helper()
	m := regexp.MustCompile("written to `([^`]+)`").FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("output does not name the report path:\n%s", out)
	}
	path := m[1]
	if !filepath.IsAbs(path) || filepath.Dir(path) != report.Dir() {
		t.Fatalf("report at %s, want an absolute path in %s", path, report.Dir())
	}
	if !strings.Contains(out, "Open: "+report.FileURL(path)) {
		t.Fatalf("output does not carry the file:// URL:\n%s", out)
	}
	page, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	return path, string(page)
}

func TestPersonRenderGraphWritesAPrivatePageBesideTheView(t *testing.T) {
	ap := graphFixture(t)

	_, out := renderEstate(t, ap, map[string]any{"view": "person", "person": "schen", "render": "graph"})

	if !strings.Contains(out, "## Estate — person view") || !strings.Contains(out, "#eng — 3 active days") {
		t.Fatalf("the markdown view is not returned with the report:\n%s", out)
	}
	path, page := readReport(t, out)
	if filepath.Base(path) != "person-schen.html" {
		t.Fatalf("report named %s", filepath.Base(path))
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("report mode %v, want 0600", fi.Mode().Perm())
		}
	}

	data := dataBlock(t, page)
	for _, want := range []string{`"label":"#eng"`, `"label":"Aaron Bockelie"`, `"kind":"seed"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("graph data missing %s:\n%s", want, data)
		}
	}
}

func TestRenderedPageNeverCarriesAHostileNameUnescaped(t *testing.T) {
	ap := graphFixture(t)

	for _, view := range []string{"person", "about"} {
		_, out := renderEstate(t, ap, map[string]any{"view": view, "person": "schen", "render": "graph"})
		_, page := readReport(t, out)
		if strings.Contains(page, hostileName) || strings.Contains(page, "<script>alert") {
			t.Fatalf("%s page carries the hostile name unescaped", view)
		}
		if !strings.Contains(dataBlock(t, page), `\u003c/script\u003e\u003cscript\u003ealert(1)`) {
			t.Fatalf("%s page dropped the hostile name instead of escaping it", view)
		}
		if n := strings.Count(page, "</script"); n != 3 {
			t.Fatalf("%s page has %d script closers, want 3", view, n)
		}
	}
}

func TestRenderedPageCarriesNoSlackIDs(t *testing.T) {
	ap := graphFixture(t)

	for _, view := range []string{"person", "about"} {
		_, out := renderEstate(t, ap, map[string]any{"view": view, "person": "schen", "render": "graph"})
		_, page := readReport(t, out)
		data := dataBlock(t, page)
		for _, id := range fixtureIDs {
			if regexp.MustCompile(`\b` + id + `\b`).MatchString(data) {
				t.Fatalf("%s graph carries Slack ID %s:\n%s", view, id, data)
			}
		}
		if m := regexp.MustCompile(`\b[UCDGW]0[A-Z0-9]{4,}\b`).FindString(data); m != "" {
			t.Fatalf("%s graph carries an ID-shaped string %s:\n%s", view, m, data)
		}
	}
}

func TestAboutRenderGraphWritesTheComposite(t *testing.T) {
	ap := graphFixture(t)

	_, out := renderEstate(t, ap, map[string]any{"view": "about", "person": "schen", "render": "graph"})
	if !strings.Contains(out, "## Estate — about") {
		t.Fatalf("the about markdown is not returned:\n%s", out)
	}
	path, page := readReport(t, out)
	if filepath.Base(path) != "about-schen.html" {
		t.Fatalf("report named %s", filepath.Base(path))
	}
	if !strings.Contains(dataBlock(t, page), `"label":"Aaron Bockelie"`) {
		t.Fatalf("about graph missing the circle:\n%s", dataBlock(t, page))
	}
}

func TestOperatorHoursNeverReachTheGraph(t *testing.T) {
	ap := graphFixture(t)

	_, out := renderEstate(t, ap, map[string]any{"view": "person", "person": "bockeliea", "render": "graph"})
	if !strings.Contains(out, "Hour-cadence parallelism") {
		t.Fatalf("fixture did not produce operator hours in the markdown:\n%s", out)
	}
	_, page := readReport(t, out)
	data := strings.ToLower(dataBlock(t, page))
	for _, hourish := range []string{"parallel", "concurrent", "hour"} {
		if strings.Contains(data, hourish) {
			t.Fatalf("graph carries hour-level data (%q):\n%s", hourish, data)
		}
	}
}

func TestRenderRefusesOtherViewsAndValues(t *testing.T) {
	ap := graphFixture(t)

	for _, params := range []map[string]any{
		{"view": "families", "render": "graph"},
		{"view": "initiatives", "render": "graph"},
		{"view": "convergence", "people": "schen,bockeliea", "render": "graph"},
		{"view": "channels", "render": "graph"},
		{"view": "people", "person": "sarah", "render": "graph"},
		{"view": "person", "person": "schen", "render": "pie"},
		{"view": "person", "person": "schen", "render": true},
	} {
		res, out := renderEstate(t, ap, params)
		if res.Success {
			t.Fatalf("render accepted for %v:\n%s", params, out)
		}
		if !strings.Contains(out, "render='graph'") {
			t.Fatalf("refusal does not name the render it supports: %s", out)
		}
	}
	if _, err := os.Stat(report.Dir()); !os.IsNotExist(err) {
		t.Fatalf("a refused render touched the reports directory: %v", err)
	}
}

func TestRenderOnAMissWritesNothing(t *testing.T) {
	ap := graphFixture(t)

	_, out := renderEstate(t, ap, map[string]any{"view": "person", "person": "nobody-at-all", "render": "graph"})
	if !strings.Contains(out, "Could not resolve") || !strings.Contains(out, "not written") {
		t.Fatalf("a miss should render the candidates and say no report was written:\n%s", out)
	}
	if _, err := os.Stat(report.Dir()); !os.IsNotExist(err) {
		t.Fatalf("a miss touched the reports directory: %v", err)
	}
}

func TestViewsWithoutRenderWriteNothing(t *testing.T) {
	ap := graphFixture(t)

	_, out := renderEstate(t, ap, map[string]any{"view": "person", "person": "schen"})
	if strings.Contains(out, "Graph report") {
		t.Fatalf("a plain read announced a report:\n%s", out)
	}
	if _, err := os.Stat(report.Dir()); !os.IsNotExist(err) {
		t.Fatalf("a plain read touched the reports directory: %v", err)
	}
}
