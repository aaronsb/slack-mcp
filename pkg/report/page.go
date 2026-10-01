package report

// The page shell. Nothing in these constants is data: every view string
// arrives through the JSON block and is drawn on the canvas by Cytoscape or
// assigned with textContent. The CSP admits inline script and style only —
// the page has no other source, so default-src 'none' costs nothing — and
// data: images because Cytoscape's canvas renderer may hand them to itself.
// No 'unsafe-eval': the vendored bundle uses neither eval nor Function.

const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:">
<meta name="referrer" content="no-referrer">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>slack-mcp graph report</title>
<style>
:root {
  --bg: #fbfaf8; --panel: #ffffff; --ink: #1d1d1f; --muted: #66676b; --rule: #e4e2dd;
  --seed: #c2410c; --person: #2563eb; --channel: #0f766e; --dm: #7c3aed; --family: #a16207;
  --edge: #9a9890; --focus: #111111;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161618; --panel: #1f1f22; --ink: #ececee; --muted: #a0a0a6; --rule: #333338;
    --seed: #fb923c; --person: #60a5fa; --channel: #2dd4bf; --dm: #a78bfa; --family: #facc15;
    --edge: #6b6b72; --focus: #ffffff;
  }
}
* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; }
body { background: var(--bg); color: var(--ink); font: 14px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; display: flex; flex-direction: column; }
header { padding: 14px 18px 10px; border-bottom: 1px solid var(--rule); }
h1 { font-size: 17px; margin: 0 0 2px; font-weight: 600; }
#subtitle { color: var(--muted); margin: 0; }
main { flex: 1; display: flex; min-height: 0; }
#graph { flex: 1; min-width: 0; min-height: 360px; }
aside { width: 300px; border-left: 1px solid var(--rule); background: var(--panel); padding: 14px 16px; overflow-y: auto; }
aside h2 { font-size: 12px; text-transform: uppercase; letter-spacing: .06em; color: var(--muted); margin: 16px 0 6px; font-weight: 600; }
aside h2:first-child { margin-top: 0; }
ul { margin: 0; padding-left: 18px; }
#legend { list-style: none; padding: 0; }
#legend li { display: flex; align-items: center; gap: 8px; margin: 3px 0; }
.sw { width: 11px; height: 11px; border-radius: 50%; display: inline-block; flex: none; }
.sw.line { border-radius: 0; height: 3px; width: 16px; background: var(--edge); }
#details-title { font-weight: 600; margin: 0 0 4px; overflow-wrap: anywhere; }
#details-kind { color: var(--muted); margin: 0 0 6px; }
#details li, #notes li { overflow-wrap: anywhere; }
.hint { color: var(--muted); }
@media (max-width: 720px) {
  main { flex-direction: column; }
  aside { width: auto; border-left: 0; border-top: 1px solid var(--rule); }
}
</style>
</head>
`

const pageBody = `<body>
<header>
<h1 id="title"></h1>
<p id="subtitle"></p>
</header>
<main>
<div id="graph" role="img" aria-label="Relationship graph"></div>
<aside>
<h2>Legend</h2>
<ul id="legend">
<li><span class="sw" style="background:var(--seed)"></span>subject</li>
<li><span class="sw" style="background:var(--person)"></span>person</li>
<li><span class="sw" style="background:var(--channel)"></span>channel</li>
<li><span class="sw" style="background:var(--dm)"></span>direct message</li>
<li><span class="sw" style="background:var(--family)"></span>channel family</li>
<li><span class="sw line"></span>edge width = observed days</li>
</ul>
<h2>Details</h2>
<div id="details">
<p class="hint" id="details-hint">Hover or tap a node or edge.</p>
<p id="details-title"></p>
<p id="details-kind"></p>
<ul id="details-lines"></ul>
</div>
<h2>Notes</h2>
<ul id="notes"></ul>
</aside>
</main>
`

const pageScript = `
(function () {
  "use strict";
  var data = JSON.parse(document.getElementById("data").textContent);
  function text(id, s) { document.getElementById(id).textContent = s || ""; }
  function list(id, items) {
    var ul = document.getElementById(id);
    while (ul.firstChild) ul.removeChild(ul.firstChild);
    (items || []).forEach(function (s) {
      var li = document.createElement("li");
      li.textContent = s;
      ul.appendChild(li);
    });
  }

  document.title = data.title;
  text("title", data.title);
  text("subtitle", data.subtitle);
  list("notes", data.notes);

  var css = getComputedStyle(document.documentElement);
  function color(name) { return css.getPropertyValue("--" + name).trim(); }
  var kindNames = { seed: "subject", person: "person", channel: "channel", dm: "direct message", family: "channel family" };

  var maxW = 1;
  data.edges.forEach(function (e) { if (e.weight > maxW) maxW = e.weight; });

  var elements = [];
  data.nodes.forEach(function (n) {
    elements.push({ group: "nodes", data: { id: n.id, label: n.label, kind: n.kind, detail: n.detail || [] } });
  });
  data.edges.forEach(function (e, i) {
    elements.push({ group: "edges", data: { id: "e" + i, source: e.source, target: e.target, kind: e.kind, weight: e.weight, label: e.label || "" } });
  });

  var cy = cytoscape({
    container: document.getElementById("graph"),
    maxZoom: 1.25,
    minZoom: 0.2,
    elements: elements,
    style: [
      { selector: "node", style: {
        "background-color": function (n) { return color(n.data("kind")) || color("person"); },
        "label": "data(label)",
        "color": color("ink"),
        "font-size": 11,
        "text-valign": "bottom",
        "text-margin-y": 4,
        "text-wrap": "ellipsis",
        "text-max-width": "150px",
        "width": 18, "height": 18
      } },
      { selector: "node[kind = 'seed']", style: { "width": 34, "height": 34, "font-size": 13, "font-weight": "bold" } },
      { selector: "node[kind = 'family']", style: { "shape": "round-rectangle" } },
      { selector: "edge", style: {
        "width": function (e) { return 1.2 + 6.8 * (e.data("weight") - 1) / Math.max(1, maxW - 1); },
        "line-color": color("edge"),
        "curve-style": "bezier",
        "opacity": 0.75
      } },
      { selector: "edge[kind = 'created'], edge[kind = 'founder']", style: { "line-style": "dashed" } },
      { selector: ".faded", style: { "opacity": 0.15 } },
      { selector: "node.focus", style: { "border-width": 3, "border-color": color("focus") } }
    ],
    // The seed at the center and every neighbor on one ring: a spoke from
    // the center never passes behind another node, so no edge is drawn
    // where none was observed. Ring order groups nodes by kind.
    layout: {
      name: "concentric",
      concentric: function (n) { return n.data("kind") === "seed" ? 2 : 1; },
      levelWidth: function () { return 1; },
      sort: function (a, b) {
        var order = { person: 0, dm: 1, channel: 2, family: 3 };
        return (order[a.data("kind")] || 0) - (order[b.data("kind")] || 0);
      },
      minNodeSpacing: 24,
      nodeDimensionsIncludeLabels: true,
      padding: 30,
      animate: false
    }
  });

  function showNode(n) {
    document.getElementById("details-hint").hidden = true;
    text("details-title", n.data("label"));
    text("details-kind", kindNames[n.data("kind")] || n.data("kind"));
    var lines = (n.data("detail") || []).slice();
    n.connectedEdges().forEach(function (e) {
      var other = e.source().id() === n.id() ? e.target() : e.source();
      lines.push(other.data("label") + (e.data("label") ? " — " + e.data("label") : ""));
    });
    list("details-lines", lines);
  }
  function showEdge(e) {
    document.getElementById("details-hint").hidden = true;
    text("details-title", e.source().data("label") + " — " + e.target().data("label"));
    text("details-kind", e.data("kind"));
    list("details-lines", e.data("label") ? [e.data("label")] : []);
  }
  function focus(n) {
    cy.elements().addClass("faded").removeClass("focus");
    n.closedNeighborhood().removeClass("faded");
    n.addClass("focus");
  }
  function clear() { cy.elements().removeClass("faded focus"); }

  cy.on("mouseover tap", "node", function (ev) { focus(ev.target); showNode(ev.target); });
  cy.on("mouseover tap", "edge", function (ev) { showEdge(ev.target); });
  cy.on("mouseout", "node", clear);
  cy.on("tap", function (ev) { if (ev.target === cy) clear(); });
})();
`
