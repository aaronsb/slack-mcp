package features

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aaronsb/slack-mcp/pkg/playbook"
	"github.com/aaronsb/slack-mcp/pkg/provider"
)

// Batch is ADR-010's executor: in the assignment rule's vocabulary a fourth
// kind — it encodes composition, never effect. It admits only the three
// read nouns, so per-tool gating on the verbs cannot be bypassed by
// wrapping them.

// batchable maps the tool names the executor admits to their features.
var batchable = map[string]*Feature{
	"inbox":    Inbox,
	"messages": Messages,
	"estate":   EstateViews,
}

const batchMaxCommands = 12

var Batch = &Feature{
	Name:        "batch",
	Description: "Execute a held plan in one call: an ordered commands array of reads (inbox, messages, estate — nothing else), rendered as one document under each item's own output laws. Items are independent — an item cannot read an earlier item's results, so a plan with data dependencies stays multi-turn. An item that fails renders its error in place; the rest still run. Playbooks: save='name' commands=[...] stores a named sequence, run='name' executes it, list=true shows them, delete='name' removes one.",
	Schema: map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"commands": map[string]interface{}{
				"type":        "array",
				"description": "Ordered commands to execute (max 12): [{tool: 'inbox'|'messages'|'estate', params: {...}}, ...]",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool":   map[string]interface{}{"type": "string"},
						"params": map[string]interface{}{"type": "object"},
					},
					"required": []string{"tool"},
				},
			},
			"save": map[string]interface{}{
				"type":        "string",
				"description": "Store commands as a named playbook (replaces the whole sequence; does not run it)",
			},
			"run": map[string]interface{}{
				"type":        "string",
				"description": "Execute a stored playbook by name",
			},
			"list": map[string]interface{}{
				"type":        "boolean",
				"description": "Show stored playbooks: names, command counts, last run",
				"default":     false,
			},
			"delete": map[string]interface{}{
				"type":        "string",
				"description": "Remove a stored playbook by name",
			},
		},
		"required": []string{},
	},
	Handler: batchHandler,
}

func batchHandler(ctx context.Context, params map[string]interface{}) (*FeatureResult, error) {
	ap, ok := params["_provider"].(*provider.ApiProvider)
	if !ok {
		return &FeatureResult{Success: false, Message: "Internal error: provider not available"}, nil
	}

	save, _ := params["save"].(string)
	run, _ := params["run"].(string)
	del, _ := params["delete"].(string)
	list, _ := params["list"].(bool)
	rawCommands, hasCommands := params["commands"]

	switch {
	case del != "":
		return batchDelete(ap, del)
	case list:
		return batchList(ap)
	case save != "":
		return batchSave(ap, save, rawCommands)
	case run != "":
		if hasCommands {
			return &FeatureResult{
				Success:  false,
				Message:  "run= executes a stored playbook and takes no commands.",
				Guidance: fmt.Sprintf("To change the sequence, re-save it whole: batch save='%s' commands=[...]", run),
			}, nil
		}
		return batchRun(ctx, ap, run)
	case hasCommands:
		cmds, errRes := parseCommands(rawCommands)
		if errRes != nil {
			return errRes, nil
		}
		doc := executeBatch(ctx, ap, cmds)
		return &FeatureResult{
			Success:     true,
			Message:     doc,
			ResultCount: len(cmds),
			Echo:        fmt.Sprintf("batch %d commands", len(cmds)),
		}, nil
	default:
		return &FeatureResult{
			Success: false,
			Message: "batch needs commands=[{tool, params}...] to execute, or one of save='name' commands=[...], run='name', list=true, delete='name'.",
		}, nil
	}
}

// parseCommands decodes and validates the commands array. Validation is
// whole-batch: a plan carrying a write or a nested batch is malformed, and
// rejecting it before item one runs beats executing half of it first.
func parseCommands(raw interface{}) ([]playbook.Command, *FeatureResult) {
	var items []map[string]interface{}
	switch v := raw.(type) {
	case []interface{}:
		for _, item := range v {
			m, ok := item.(map[string]interface{})
			if !ok {
				return nil, &FeatureResult{Success: false, Message: "Each command must be an object: {tool: '...', params: {...}}"}
			}
			items = append(items, m)
		}
	case []map[string]interface{}:
		items = v
	default:
		return nil, &FeatureResult{Success: false, Message: "commands must be an array: [{tool: '...', params: {...}}, ...]"}
	}

	if len(items) == 0 {
		return nil, &FeatureResult{Success: false, Message: "commands is empty — a batch needs at least one command."}
	}
	if len(items) > batchMaxCommands {
		return nil, &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("%d commands exceeds the cap of %d.", len(items), batchMaxCommands),
			Guidance: fmt.Sprintf("Split the plan: run the first %d, then a second batch with the rest.", batchMaxCommands),
		}
	}

	cmds := make([]playbook.Command, 0, len(items))
	for i, m := range items {
		tool, _ := m["tool"].(string)
		if tool == "batch" {
			return nil, &FeatureResult{Success: false, Message: "batch may not contain batch."}
		}
		if _, ok := batchable[tool]; !ok {
			return nil, &FeatureResult{
				Success: false,
				Message: fmt.Sprintf("Command %d: %q is not batchable — the executor admits only reads: inbox, messages, estate.", i+1, tool),
			}
		}
		p, _ := m["params"].(map[string]interface{})
		cmds = append(cmds, playbook.Command{Tool: tool, Params: p})
	}
	return cmds, nil
}

// executeBatch runs the commands in order and renders one document: each
// item's output under its own laws, opening with its echo line, separated
// by horizontal rules. A failing item renders its error in place and the
// rest still run — one unreadable channel must not cost the plan.
func executeBatch(ctx context.Context, ap *provider.ApiProvider, cmds []playbook.Command) string {
	sections := make([]string, 0, len(cmds))
	for _, c := range cmds {
		f := batchable[c.Tool]
		p := make(map[string]interface{}, len(c.Params)+1)
		for k, v := range c.Params {
			p[k] = v
		}
		p["_provider"] = ap

		res, err := f.Handler(ctx, p)
		switch {
		case err != nil:
			sections = append(sections, "`"+commandEcho(c)+"`\n\n**Error:** "+err.Error())
		case res == nil:
			sections = append(sections, "`"+commandEcho(c)+"`\n\n**Error:** no result")
		default:
			if res.Echo == "" {
				res.Echo = commandEcho(c)
			}
			sections = append(sections, FormatResult(f.Name, res))
		}
	}
	return strings.Join(sections, "\n\n---\n\n")
}

// commandEcho reconstructs an item's effective-invocation line for results
// that did not stamp their own (usage errors from the dispatchers).
func commandEcho(c playbook.Command) string {
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{c.Tool}
	for _, k := range keys {
		if s, ok := c.Params[k].(string); ok {
			parts = append(parts, fmt.Sprintf("%s='%s'", k, s))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%v", k, c.Params[k]))
		}
	}
	return strings.Join(parts, " ")
}

// ---- playbooks ----

func openPlaybooks(ap *provider.ApiProvider) (*playbook.Store, *FeatureResult) {
	identity := ap.ProvideIdentity()
	if identity == nil || identity.Team == "" {
		return nil, &FeatureResult{
			Success: false,
			Message: "Could not determine which workspace this is; playbooks are stored per workspace.",
		}
	}
	store, err := playbook.Open(identity.Team)
	if err != nil {
		return nil, &FeatureResult{Success: false, Message: fmt.Sprintf("Could not open the playbook store: %v", err)}
	}
	return store, nil
}

func batchSave(ap *provider.ApiProvider, name string, rawCommands interface{}) (*FeatureResult, error) {
	if rawCommands == nil {
		return &FeatureResult{
			Success: false,
			Message: fmt.Sprintf("save='%s' needs commands=[...] — the sequence to store.", name),
		}, nil
	}
	cmds, errRes := parseCommands(rawCommands)
	if errRes != nil {
		return errRes, nil
	}
	store, errRes := openPlaybooks(ap)
	if errRes != nil {
		return errRes, nil
	}
	if err := store.Save(name, cmds, time.Now()); err != nil {
		return &FeatureResult{Success: false, Message: fmt.Sprintf("Could not save the playbook: %v", err)}, nil
	}
	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Playbook '%s' saved: %d commands. Saved, not run — batch run='%s' executes it.", name, len(cmds), name),
		Echo:    fmt.Sprintf("batch save='%s' %d commands", name, len(cmds)),
	}, nil
}

func batchRun(ctx context.Context, ap *provider.ApiProvider, name string) (*FeatureResult, error) {
	store, errRes := openPlaybooks(ap)
	if errRes != nil {
		return errRes, nil
	}
	cmds, ok := store.Get(name)
	if !ok {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("No playbook named '%s'.", name),
			Guidance: "batch list=true shows what exists.",
		}, nil
	}
	doc := executeBatch(ctx, ap, cmds)
	// Best-effort bookkeeping: the run already happened, and the output
	// claims nothing about statistics.
	_ = store.MarkRun(name, time.Now())
	return &FeatureResult{
		Success:     true,
		Message:     doc,
		ResultCount: len(cmds),
		Echo:        fmt.Sprintf("batch run='%s' %d commands", name, len(cmds)),
	}, nil
}

func batchList(ap *provider.ApiProvider) (*FeatureResult, error) {
	store, errRes := openPlaybooks(ap)
	if errRes != nil {
		return errRes, nil
	}
	summaries := store.List()
	if len(summaries) == 0 {
		return &FeatureResult{
			Success: true,
			Message: "No playbooks saved. batch save='name' commands=[...] stores one.",
			Echo:    "batch list",
		}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Playbooks (%d)\n\n", len(summaries))
	for _, s := range summaries {
		fmt.Fprintf(&b, "**%s** — %d commands, saved %s", s.Name, s.Commands, s.SavedAt.Format("2006-01-02"))
		if s.LastRun != nil {
			fmt.Fprintf(&b, ", last run %s (%d runs)", s.LastRun.Format("2006-01-02"), s.Runs)
		} else {
			b.WriteString(", never run")
		}
		b.WriteString("\n")
	}
	return &FeatureResult{
		Success:     true,
		Message:     strings.TrimRight(b.String(), "\n"),
		ResultCount: len(summaries),
		Echo:        "batch list",
	}, nil
}

func batchDelete(ap *provider.ApiProvider, name string) (*FeatureResult, error) {
	store, errRes := openPlaybooks(ap)
	if errRes != nil {
		return errRes, nil
	}
	found, err := store.Delete(name)
	if err != nil {
		return &FeatureResult{Success: false, Message: fmt.Sprintf("Could not delete the playbook: %v", err)}, nil
	}
	if !found {
		return &FeatureResult{
			Success:  false,
			Message:  fmt.Sprintf("No playbook named '%s'.", name),
			Guidance: "batch list=true shows what exists.",
		}, nil
	}
	return &FeatureResult{
		Success: true,
		Message: fmt.Sprintf("Playbook '%s' deleted.", name),
		Echo:    fmt.Sprintf("batch delete='%s'", name),
	}, nil
}

// ---- the frequency hint ----

// ReadPacer watches tool-call timing for the signature of an agent
// executing a held plan one call at a time: a read landing within two
// seconds of the previous read. The server owns one and feeds it every
// tool call; a match earns one footer line naming the batch equivalent,
// and everything else stays silent.
type ReadPacer struct {
	mu       sync.Mutex
	lastTool string
	lastAt   time.Time
}

const pacerWindow = 2 * time.Second

// Observe records a tool call and returns the hint line, or "" when the
// pattern is absent. Non-read tools (batch included) reset the streak: a
// read following a write is a reaction, not a held plan.
func (p *ReadPacer) Observe(tool string, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := batchable[tool]; !ok {
		p.lastTool, p.lastAt = "", time.Time{}
		return ""
	}

	prevTool, prevAt := p.lastTool, p.lastAt
	p.lastTool, p.lastAt = tool, now
	if prevTool == "" || now.Sub(prevAt) >= pacerWindow {
		return ""
	}
	return fmt.Sprintf("Two reads within 2s (%s, then %s) — a held plan runs in one call: batch commands=[{tool:'%s',params:{...}},{tool:'%s',params:{...}}]",
		prevTool, tool, prevTool, tool)
}
