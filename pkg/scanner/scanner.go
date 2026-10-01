// Package scanner is ADR-013's layer 2: a deterministic secret scanner over
// everything a `say` would send.
//
// The scanner matches a fixed set of pattern classes (patterns.go, env.go)
// against each field as sent, then decodes encoded content (decode.go) and
// matches the decoded bytes again, breadth-first to depth 3 under one decode
// budget per call. No model is in the loop: the same input always gives the
// same Result.
//
// What the agent may be told and what the operator may be told are kept
// apart. A Finding names the class and the field and nothing else: no
// matched value, no offset, no decode chain. The offset and decode chain the
// quarantine file records (ADR-013, "The quarantine file") are available only
// through Result.Record, for the caller to write there and never to return
// to the agent. Nothing in this package ever exposes the matched value.
package scanner

import (
	"sort"
)

// DefaultBudget is the decode budget per call: 32 MiB of decoded output plus
// the input every inflate attempt consumes (ADR-013, Decoding).
const DefaultBudget int64 = 32 << 20

// DefaultMaxDepth is how deep decoding goes. The content as sent is depth 0;
// output at depth 3 is matched but not decoded again.
const DefaultMaxDepth = 3

// FieldKind says what part of a `say` a Field carries. The kinds are declared
// in scan order: ADR-013 scans the text forms first, then the file names,
// then each file's bytes. Scan sorts its input stably by kind, so the order
// the caller passes fields in matters only within a kind.
type FieldKind int

const (
	// FieldText is one form of the message text: as the agent supplied it,
	// the fallback text the server posts, or the joined plain text of one
	// rich_text element. Index tells forms apart for the caller.
	FieldText FieldKind = iota
	// FieldEmoji is a reaction's emoji name.
	FieldEmoji
	// FieldTitle is an upload's title.
	FieldTitle
	// FieldComment is an upload's initial comment.
	FieldComment
	// FieldFileName is an attached file's name; Index is its position.
	FieldFileName
	// FieldFileBytes is an attached file's bytes; Index is its position.
	FieldFileBytes
)

// String names the kind for logs and the quarantine record.
func (k FieldKind) String() string {
	switch k {
	case FieldText:
		return "text"
	case FieldEmoji:
		return "emoji"
	case FieldTitle:
		return "title"
	case FieldComment:
		return "comment"
	case FieldFileName:
		return "file-name"
	case FieldFileBytes:
		return "file-bytes"
	}
	return "unknown"
}

// Field is one piece of content a `say` would send.
type Field struct {
	Kind  FieldKind
	Index int
	Data  []byte
	// ElementStarts marks, in a joined rich_text form, the offset where each
	// inline element begins. The env-secret class counts each as a line
	// start, so a whole code element (`API_TOKEN=…`) is caught inside a
	// sentence (ADR-013, Env secret). Nil for every other field.
	ElementStarts []int
}

// FieldRef identifies a field without carrying its content.
type FieldRef struct {
	Kind  FieldKind
	Index int
}

// Ref returns the field's identity without its content.
func (f Field) Ref() FieldRef { return FieldRef{Kind: f.Kind, Index: f.Index} }

// Class is a pattern class from ADR-013's table. Its value is the name the
// log line and the quarantine file use (`class=private-key`).
type Class string

// The pattern classes, in ADR-013's table order. When two classes match at
// the same offset, the earlier one is reported.
const (
	ClassPrivateKey    Class = "private-key"
	ClassSlackToken    Class = "slack-token"
	ClassSlackCookie   Class = "slack-cookie"
	ClassSlackWebhook  Class = "slack-webhook"
	ClassAWSAccessKey  Class = "aws-access-key"
	ClassGitHubToken   Class = "github-token"
	ClassModelKey      Class = "model-provider-key"
	ClassJWT           Class = "jwt"
	ClassCredentialURL Class = "credential-url"
	ClassEnvSecret     Class = "env-secret"
)

// Finding is a block: the class that matched and the field it matched in.
// It deliberately carries nothing else (ADR-013, Layer 4).
type Finding struct {
	Class Class
	Field FieldRef
}

// Decoder names one decoding step.
type Decoder string

// The decoders, in their pinned order.
const (
	DecoderBase64 Decoder = "base64"
	DecoderHex    Decoder = "hex"
	DecoderURL    Decoder = "url"
	DecoderGzip   Decoder = "gzip"
	DecoderZlib   Decoder = "zlib"
)

// Step is one decoding step in the chain that exposed a match.
type Step struct {
	Decoder Decoder
	// Offset is where the decoded span starts in its parent buffer: the
	// field's bytes as sent for the first step, the previous step's output
	// after that.
	Offset int
	// Align is the number of leading characters dropped: 0-3 for base64,
	// 0-1 for hex, 0 otherwise.
	Align int
}

// Record is what the quarantine file records about a block: the class, the
// field, the offset, and the decode chain. It is for the operator's record
// only and must never reach the agent. It still never carries the matched
// value.
type Record struct {
	Class Class
	Field FieldRef
	// Offset is where the match starts in the innermost buffer: the field's
	// bytes when Chain is empty, else the last step's output.
	Offset int
	// Chain is the decode chain from the field as sent to the buffer the
	// match was found in; empty for a match in the content as sent.
	Chain []Step
}

// Options tunes a scan. The zero value gives ADR-013's settings.
type Options struct {
	// Budget is the decode budget in bytes; 0 means DefaultBudget.
	Budget int64
	// MaxDepth is the deepest decoded output; 0 means DefaultMaxDepth.
	MaxDepth int
}

// Result is the outcome of a scan. Exactly one of three holds: clean (no
// findings, scannable), blocked (one finding), or unscannable.
type Result struct {
	// Findings holds the block, if any. The first match ends the scan, so
	// it has at most one entry.
	Findings []Finding
	// Unscannable is set when the budget ran out without a match. Nothing
	// may be sent, and no quarantine or strike follows.
	Unscannable bool
	// UnscannableField is the field whose decoding exhausted the budget.
	UnscannableField *FieldRef

	budgetUsed int64
	record     *Record
}

// Blocked reports whether a pattern matched.
func (r Result) Blocked() bool { return len(r.Findings) > 0 }

// BudgetUsedForLog is how much of the decode budget the scan charged. It is
// for the operator's log only: an agent told how close a refused call came
// to the budget learns how to size a split (ADR-013, Layer 4).
func (r Result) BudgetUsedForLog() int64 { return r.budgetUsed }

// Record returns the quarantine-file record of a block. It is for the
// operator's record only; never pass it, or anything derived from it beyond
// the Finding, to the agent.
func (r Result) Record() (Record, bool) {
	if r.record == nil {
		return Record{}, false
	}
	rec := *r.record
	rec.Chain = append([]Step(nil), r.record.Chain...)
	return rec, true
}

// node is one buffer in the breadth-first scan.
type node struct {
	data          []byte
	field         int // index into the sorted fields
	depth         int
	parent        *node // nil at depth 0
	step          Step  // how parent's bytes decoded to these
	elementStarts []int
}

// chain walks n's parents to the decode chain from the field as sent. It is
// built only for a block.
func (n *node) chain() []Step {
	chain := make([]Step, n.depth)
	for p := n; p.parent != nil; p = p.parent {
		chain[p.depth-1] = p.step
	}
	return chain
}

// Scan scans every field and returns the first match, or reports the content
// unscannable when the budget runs out without one.
//
// Order is fixed (ADR-013): every field at depth 0 in kind order, then all of
// depth 1, and so on; within a buffer, decoded spans in offset order and
// decoders in their pinned order. Every decoded buffer is matched as soon as
// it is produced, so every byte charged to the budget is matched before the
// budget can run out: a match within budget always blocks.
func Scan(fields []Field, opts Options) Result {
	s := newScan(opts)
	sorted := make([]Field, len(fields))
	copy(sorted, fields)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Kind < sorted[j].Kind })

	level := make([]*node, 0, len(sorted))
	for i, f := range sorted {
		n := &node{data: f.Data, field: i, elementStarts: f.ElementStarts}
		if class, off, ok := match(n.data, n.elementStarts); ok {
			return s.block(sorted, n, class, off)
		}
		level = append(level, n)
	}

	for depth := 0; depth < s.maxDepth && len(level) > 0; depth++ {
		var next []*node
		for _, n := range level {
			inflate := depth > 0 || sorted[n.field].Kind == FieldFileBytes
			var found *Result
			exhausted := s.decode(n, inflate, func(child *node) bool {
				if class, off, ok := match(child.data, nil); ok {
					r := s.block(sorted, child, class, off)
					found = &r
					return false
				}
				if child.depth < s.maxDepth {
					next = append(next, child)
				}
				return true
			})
			if found != nil {
				return *found
			}
			if exhausted {
				ref := sorted[n.field].Ref()
				return Result{Unscannable: true, UnscannableField: &ref, budgetUsed: s.used}
			}
		}
		level = next
	}
	return Result{budgetUsed: s.used}
}

func (s *scan) block(fields []Field, n *node, class Class, off int) Result {
	ref := fields[n.field].Ref()
	return Result{
		Findings:   []Finding{{Class: class, Field: ref}},
		budgetUsed: s.used,
		record:     &Record{Class: class, Field: ref, Offset: off, Chain: n.chain()},
	}
}
