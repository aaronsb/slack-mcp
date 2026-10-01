package scanner

import (
	"bytes"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// findEnvSecret matches a line `NAME = value` whose NAME names a secret and
// whose value looks like one (ADR-013, Env secret). Lines start at the input's
// start and after each `\n`; in a joined rich_text form, each inline
// element's start also counts as a line start.
//
// Each candidate is parsed within its own segment: a line start's segment is
// its line, an element start's runs to the next element start or the line's
// end, whichever comes first. Element segments partition the line, so the
// work stays linear however many elements a line holds; and a code element
// inside a sentence is judged on its own.
func findEnvSecret(b []byte, elementStarts []int) int {
	var starts []int
	if len(elementStarts) > 0 {
		starts = append([]int(nil), elementStarts...)
		sort.Ints(starts)
	}
	ei := 0
	for line := 0; line < len(b); {
		end := lineEnd(b, line)
		for ei < len(starts) && starts[ei] < line {
			ei++
		}
		// The line start, and any element start at the same offset.
		if envSegment(b[line:end]) {
			return line
		}
		for ei < len(starts) && starts[ei] < end {
			s := starts[ei]
			for ei < len(starts) && starts[ei] == s {
				ei++
			}
			segEnd := end
			if ei < len(starts) && starts[ei] < end {
				segEnd = starts[ei]
			}
			if envSegment(b[s:segEnd]) {
				return s
			}
		}
		line = end + 1
	}
	return -1
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// envSegment reports whether seg, after leading spaces and tabs and an
// optional `export `, is `NAME = value` with a secret NAME and a
// secret-looking value. It works on the bytes and stops early: NAME and `=`
// are parsed first, and an unquoted value ends at the first whitespace.
func envSegment(seg []byte) bool {
	i := 0
	for i < len(seg) && isBlank(seg[i]) {
		i++
	}
	if bytes.HasPrefix(seg[i:], []byte("export ")) {
		i += len("export ")
		for i < len(seg) && isBlank(seg[i]) {
			i++
		}
	}
	n := i
	if n >= len(seg) || !(seg[n] == '_' || isLetter(seg[n])) {
		return false
	}
	for n < len(seg) && (seg[n] == '_' || isAlnum(seg[n])) {
		n++
	}
	name := seg[i:n]
	for n < len(seg) && isBlank(seg[n]) {
		n++
	}
	if n >= len(seg) || seg[n] != '=' {
		return false
	}
	if !secretName(string(name)) {
		return false
	}
	n++
	for n < len(seg) && isBlank(seg[n]) {
		n++
	}
	v, ok := envValue(seg[n:])
	return ok && secretValue(string(v))
}

// envLine is envSegment over one line, for tests and callers that hold one.
func envLine(line []byte) bool { return envSegment(line) }

// envValue takes the value without surrounding quotes. An unquoted value
// ends at the first whitespace; it holds whitespace (and does not match)
// unless only blanks, a line ending, or a comment (`#` after a space, tab,
// or carriage return) follow.
func envValue(raw []byte) ([]byte, bool) {
	if len(raw) > 0 && (raw[0] == '"' || raw[0] == '\'') {
		if j := bytes.IndexByte(raw[1:], raw[0]); j >= 0 {
			return raw[1 : 1+j], true
		}
		return bytes.TrimRight(raw[1:], "\r"), true
	}
	w := 0
	for w < len(raw) && !isSpace(raw[w]) {
		w++
	}
	k := w
	for k < len(raw) && (isBlank(raw[k]) || raw[k] == '\r') {
		k++
	}
	if k < len(raw) && !(raw[k] == '#' && k > w) {
		return nil, false // whitespace inside the value
	}
	return raw[:w], true
}

var secretWords = map[string]bool{
	"SECRET": true, "SECRETS": true, "PASSWORD": true, "PASSWD": true, "PASS": true,
	"PWD": true, "TOKEN": true, "KEY": true, "APIKEY": true, "SECRETKEY": true,
	"ACCESSKEY": true, "PRIVATEKEY": true, "AUTHTOKEN": true, "CREDENTIAL": true,
	"CREDENTIALS": true, "AUTH": true,
}

// secretName splits NAME into words at `_` and at each lowercase-to-uppercase
// step. It is a secret name when a word is a secret word and no word is
// `PUBLIC` or `PUB`, case-insensitive. Whole words keep MONKEY, AUTHOR, and
// BYPASS out.
func secretName(name string) bool {
	found := false
	for _, w := range nameWords(name) {
		u := strings.ToUpper(w)
		if u == "PUBLIC" || u == "PUB" {
			return false
		}
		if secretWords[u] {
			found = true
		}
	}
	return found
}

func nameWords(name string) []string {
	var words []string
	s := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '_' {
			if i > s {
				words = append(words, name[s:i])
			}
			s = i + 1
			continue
		}
		if i > s && isLower(name[i-1]) && isUpper(name[i]) {
			words = append(words, name[s:i])
			s = i
		}
	}
	return words
}

func isLower(c byte) bool  { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return isLower(c) || isUpper(c) }

// secretValue applies ADR-013's value tests, cheapest first: length,
// whitespace, the shape exclusions, and entropy last.
func secretValue(v string) bool {
	if utf8.RuneCountInString(v) < 16 {
		return false
	}
	if strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return false
	}
	if allDigits(v) || isPlaceholder(v) {
		return false
	}
	if isURL(v) || strings.HasPrefix(v, "arn:") || isPath(v) {
		return false
	}
	if identifierShaped(v) {
		return false
	}
	return entropy(v) >= 3.0
}

// entropy is the Shannon entropy of v over its characters, in bits per
// character. The terms are summed over the sorted counts, so the float result
// does not depend on map iteration order.
func entropy(v string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range v {
		counts[r]++
		n++
	}
	if n == 0 {
		return 0
	}
	sorted := make([]int, 0, len(counts))
	for _, c := range counts {
		sorted = append(sorted, c)
	}
	sort.Ints(sorted)
	h := 0.0
	for _, c := range sorted {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

func allDigits(v string) bool {
	for i := 0; i < len(v); i++ {
		if !isDigit(v[i]) {
			return false
		}
	}
	return v != ""
}

var placeholderWords = []string{"changeme", "change_me", "example", "placeholder", "redacted", "your_", "your-"}

var placeholderWraps = [][2]string{{"${", "}"}, {"$(", ")"}, {"{{", "}}"}, {"<", ">"}, {"%", "%"}}

// isPlaceholder reports whether v is a placeholder: empty; containing a
// placeholder word, case-insensitive; only `x`, `X`, `*`, `.`, or `-`; or
// wrapped as `${…}`, `$(…)`, `{{…}}`, `<…>`, or `%…%`.
func isPlaceholder(v string) bool {
	if v == "" {
		return true
	}
	lower := strings.ToLower(v)
	for _, w := range placeholderWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	if strings.Trim(v, "xX*.-") == "" {
		return true
	}
	for _, w := range placeholderWraps {
		if len(v) >= len(w[0])+len(w[1]) && strings.HasPrefix(v, w[0]) && strings.HasSuffix(v, w[1]) {
			return true
		}
	}
	return false
}

// isURL reports whether v begins `scheme://`. A URL with a real password is
// the credential-URL class's to match, whatever the name; the env class
// leaves every URL to it.
func isURL(v string) bool {
	i := strings.Index(v, "://")
	if i <= 0 || !isLetter(v[0]) {
		return false
	}
	for j := 1; j < i; j++ {
		if !schemeSet[v[j]] {
			return false
		}
	}
	return true
}

// isPath reports whether v begins `/`, `./`, `../`, `~/`, `\\`, or a drive
// letter and `:\` or `:/`.
func isPath(v string) bool {
	for _, p := range []string{"/", "./", "../", "~/", `\\`} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return len(v) >= 3 && isLetter(v[0]) && v[1] == ':' && (v[2] == '\\' || v[2] == '/')
}

// identifierShaped reports whether v holds only [A-Za-z0-9._-] and, split
// into tokens at `.`, `_`, `-`, each letter-digit step, each
// lowercase-to-uppercase step, and before the last capital of a capital run
// followed by a lowercase letter, has no single-letter letter token.
func identifierShaped(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !isAlnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	tokStart := 0
	check := func(end int) bool {
		// A single letter is a lone letter token.
		return !(end-tokStart == 1 && isLetter(v[tokStart]))
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '.' || c == '_' || c == '-' {
			if !check(i) {
				return false
			}
			tokStart = i + 1
			continue
		}
		if i == tokStart {
			continue
		}
		p := v[i-1]
		split := isDigit(p) != isDigit(c) ||
			isLower(p) && isUpper(c) ||
			isUpper(p) && isUpper(c) && i+1 < len(v) && isLower(v[i+1])
		if split {
			if !check(i) {
				return false
			}
			tokStart = i
		}
	}
	return check(len(v))
}
