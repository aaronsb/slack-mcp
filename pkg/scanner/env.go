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
// element's start also counts as a line start. A value runs to the end of its
// line; for a NAME at an element start, the value ending where the next
// element starts is tried as well, so a code element inside a sentence is
// judged on its own.
func findEnvSecret(b []byte, elementStarts []int) int {
	starts := elementStarts
	if len(starts) > 0 {
		starts = append([]int(nil), elementStarts...)
		sort.Ints(starts)
	}
	ei := 0
	nextElement := func(after int) int {
		k := sort.SearchInts(starts, after+1)
		if k < len(starts) {
			return starts[k]
		}
		return len(b)
	}
	try := func(s int, element bool) bool {
		end := lineEnd(b, s)
		if envLine(b[s:end]) {
			return true
		}
		if element {
			if e := nextElement(s); e < end {
				return envLine(b[s:e])
			}
		}
		return false
	}
	// Walk line starts and element starts together, in offset order.
	line := 0
	for {
		for ei < len(starts) && starts[ei] < line {
			if s := starts[ei]; s >= 0 && (ei == 0 || starts[ei-1] != s) && try(s, true) {
				return s
			}
			ei++
		}
		if line >= len(b) {
			break
		}
		element := false
		for ei < len(starts) && starts[ei] == line {
			element = true
			ei++
		}
		if try(line, element) {
			return line
		}
		nl := bytes.IndexByte(b[line:], '\n')
		if nl < 0 {
			break
		}
		line += nl + 1
	}
	for ; ei < len(starts); ei++ {
		if s := starts[ei]; s >= 0 && s < len(b) && (ei == 0 || starts[ei-1] != s) && try(s, true) {
			return s
		}
	}
	return -1
}

// envLine reports whether line, after leading whitespace and an optional
// `export `, is `NAME = value` with a secret NAME and a secret-looking value.
func envLine(line []byte) bool {
	l := strings.TrimRight(string(line), "\r")
	l = strings.TrimLeft(l, " \t")
	if strings.HasPrefix(l, "export ") {
		l = strings.TrimLeft(l[len("export "):], " \t")
	}
	n := 0
	for n < len(l) && (l[n] == '_' || l[n] >= 'A' && l[n] <= 'Z' || l[n] >= 'a' && l[n] <= 'z' ||
		n > 0 && l[n] >= '0' && l[n] <= '9') {
		n++
	}
	if n == 0 {
		return false
	}
	name := l[:n]
	rest := strings.TrimLeft(l[n:], " \t")
	if !strings.HasPrefix(rest, "=") {
		return false
	}
	if !secretName(name) {
		return false
	}
	return secretValue(envValue(strings.TrimLeft(rest[1:], " \t")))
}

// envValue takes the value without surrounding quotes; an unquoted value
// ends at ` #`.
func envValue(raw string) string {
	if len(raw) > 0 && (raw[0] == '"' || raw[0] == '\'') {
		if j := strings.IndexByte(raw[1:], raw[0]); j >= 0 {
			return raw[1 : 1+j]
		}
		return raw[1:]
	}
	if j := strings.Index(raw, " #"); j >= 0 {
		raw = raw[:j]
	}
	return strings.TrimRight(raw, " \t")
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

// secretValue applies ADR-013's value tests.
func secretValue(v string) bool {
	if utf8.RuneCountInString(v) < 16 {
		return false
	}
	if entropy(v) < 3.0 {
		return false
	}
	if allDigits(v) || isPlaceholder(v) {
		return false
	}
	if strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return false
	}
	if isURL(v) || strings.HasPrefix(v, "arn:") || isPath(v) {
		return false
	}
	return !identifierShaped(v)
}

// entropy is the Shannon entropy of v over its characters, in bits per
// character.
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
	h := 0.0
	for _, c := range counts {
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
