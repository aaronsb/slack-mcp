package scanner

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// A left boundary means a match starts the input or follows a byte outside
// [A-Za-z0-9]; a right boundary means it ends the input or precedes one.

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func leftBoundary(b []byte, i int) bool { return i == 0 || !isAlnum(b[i-1]) }

func rightBoundary(b []byte, i int) bool { return i >= len(b) || !isAlnum(b[i]) }

// runLen counts the bytes of set starting at b[i].
func runLen(b []byte, i int, set *[256]bool) int {
	j := i
	for j < len(b) && set[b[j]] {
		j++
	}
	return j - i
}

var (
	alnumSet      = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
	alnumDashSet  = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-")
	alnumUnderSet = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_")
	b64urlSet     = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-")
	cookieSet     = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789%/+=_-")
	webhookSet    = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/")
	upperDigitSet = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	labelSet      = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ")
	lowerSet      = byteSet("abcdefghijklmnopqrstuvwxyz")
	pemBodySet    = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=")
	schemeSet     = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+.-")
)

// finder returns the offset of the first match of one class in b, or -1.
type finder struct {
	class Class
	find  func(b []byte, elementStarts []int) int
}

// finders run in ADR-013's table order. Slack token and Slack cookie share
// one finder because a `xoxd-` string can be either; findSlack reports
// which.
var finders = []finder{
	{ClassPrivateKey, func(b []byte, _ []int) int { return findPrivateKey(b) }},
	{ClassSlackToken, func(b []byte, _ []int) int { return findSlack(b, false) }},
	{ClassSlackCookie, func(b []byte, _ []int) int { return findSlack(b, true) }},
	{ClassSlackWebhook, func(b []byte, _ []int) int { return findWebhook(b) }},
	{ClassAWSAccessKey, func(b []byte, _ []int) int { return findAWS(b) }},
	{ClassGitHubToken, func(b []byte, _ []int) int { return findGitHub(b) }},
	{ClassModelKey, func(b []byte, _ []int) int { return findModelKey(b) }},
	{ClassJWT, func(b []byte, _ []int) int { return findJWT(b) }},
	{ClassCredentialURL, func(b []byte, _ []int) int { return findCredentialURL(b) }},
	{ClassEnvSecret, findEnvSecret},
}

// match runs every class over b and returns the earliest match, ties going
// to the earlier class in table order.
func match(b []byte, elementStarts []int) (Class, int, bool) {
	best, bestOff := Class(""), -1
	for _, f := range finders {
		if off := f.find(b, elementStarts); off >= 0 && (bestOff < 0 || off < bestOff) {
			best, bestOff = f.class, off
		}
	}
	return best, bestOff, bestOff >= 0
}

// indexAll calls fn for each occurrence of sep in b, stopping when fn
// returns true, and returns that offset or -1.
func indexAll(b []byte, sep string, fn func(i int) bool) int {
	s := []byte(sep)
	for from := 0; from < len(b); {
		j := bytes.Index(b[from:], s)
		if j < 0 {
			return -1
		}
		i := from + j
		if fn(i) {
			return i
		}
		from = i + 1
	}
	return -1
}

// ---- private key ----

// findPrivateKey matches a PEM or PGP private-key header followed within 512
// bytes by at least 64 base64-alphabet characters (line breaks and
// `Key: value` header lines ignored; a JSON-escaped `\n` or `\r\n`, as in
// a cloud service-account key file, counts as a line break), or a PuTTY key
// whose `Private-Lines: N` falls within 4096 bytes of its header and whose N
// lines after it hold at least 40 base64-alphabet characters. A header with
// no body does not match.
func findPrivateKey(b []byte) int {
	pem := indexAll(b, "-----BEGIN ", func(i int) bool {
		j := i + len("-----BEGIN ")
		k := j + runLen(b, j, labelSet)
		label := b[j:k]
		if !bytes.HasPrefix(b[k:], []byte("-----")) {
			return false
		}
		if !bytes.HasSuffix(label, []byte("PRIVATE KEY")) && string(label) != "PGP PRIVATE KEY BLOCK" {
			return false
		}
		start := k + 5
		end := min(start+512, len(b))
		return bodyChars(unescapeLineBreaks(b[start:end]), true) >= 64
	})
	// The next `Private-Lines:` at or after the current header, found once
	// and reused, so a run of PuTTY headers is not searched quadratically.
	privateLines := -1
	putty := indexAll(b, "PuTTY-User-Key-File-", func(i int) bool {
		if privateLines < i {
			if j := bytes.Index(b[i:], []byte("Private-Lines:")); j >= 0 {
				privateLines = i + j
			} else {
				privateLines = len(b)
			}
		}
		if privateLines >= len(b) || privateLines-i > 4096 {
			return false
		}
		return puttyBody(b, privateLines+len("Private-Lines:"))
	})
	return minOffset(pem, putty)
}

// unescapeLineBreaks returns w with each JSON-escaped `\r\n` or `\n`
// replaced by a line break. w is at most 512 bytes.
func unescapeLineBreaks(w []byte) []byte {
	if !bytes.Contains(w, []byte(`\n`)) {
		return w
	}
	w = bytes.ReplaceAll(w, []byte(`\r\n`), []byte("\n"))
	return bytes.ReplaceAll(w, []byte(`\n`), []byte("\n"))
}

// puttyMinBody is the fewest base64 characters a PuTTY private body must
// hold: an Ed25519 key's single private line is 44.
const puttyMinBody = 40

// puttyBody reads `N` after `Private-Lines:` at p and counts the
// base64-alphabet characters on the N lines after it, stopping at the first
// character outside the alphabet and as soon as puttyMinBody is reached, so a
// check reads a bounded number of bytes.
func puttyBody(b []byte, p int) bool {
	for p < len(b) && isBlank(b[p]) {
		p++
	}
	lines, digits := 0, 0
	for p < len(b) && isDigit(b[p]) && digits < 6 {
		lines = lines*10 + int(b[p]-'0')
		p++
		digits++
	}
	if lines == 0 {
		return false
	}
	for p < len(b) && (isBlank(b[p]) || b[p] == '\r') {
		p++
	}
	if p >= len(b) || b[p] != '\n' {
		return false
	}
	p++
	n := 0
	for line := 0; line < lines && p < len(b); line++ {
		for p < len(b) && pemBodySet[b[p]] {
			n++
			p++
			if n >= puttyMinBody {
				return true
			}
		}
		if p < len(b) && b[p] == '\r' {
			p++
		}
		if p >= len(b) || b[p] != '\n' {
			return false
		}
		p++
	}
	return false
}

// bodyChars counts the base64-alphabet characters at the start of w, line by
// line, stopping at the first character outside the alphabet or (without
// headers) the first line that is not wholly alphabet. With headers, empty
// lines and `Key: value` lines are skipped. It stops counting at 64.
func bodyChars(w []byte, headers bool) int {
	n := 0
	for len(w) > 0 && n < 64 {
		line := w
		if j := bytes.IndexByte(w, '\n'); j >= 0 {
			line, w = w[:j], w[j+1:]
		} else {
			w = nil
		}
		line = bytes.Trim(line, " \t\r")
		if len(line) == 0 {
			if headers {
				continue
			}
			return n
		}
		if headers && isHeaderLine(line) {
			continue
		}
		k := runLen(line, 0, pemBodySet)
		n += k
		if k < len(line) {
			return n
		}
	}
	return n
}

// isHeaderLine reports whether line is `Key: value` (Proc-Type, DEK-Info,
// an armor header).
func isHeaderLine(line []byte) bool {
	c := bytes.IndexByte(line, ':')
	if c <= 0 {
		return false
	}
	for _, ch := range line[:c] {
		if !isAlnum(ch) && ch != '-' {
			return false
		}
	}
	return true
}

// ---- Slack ----

// findSlack matches a Slack token (`xox[abcdeoprs]-` or `xapp-` and at least
// 20 of [A-Za-z0-9-]) or, with cookie set, a Slack cookie (`xoxd-` and at
// least 20 of [A-Za-z0-9%/+=_-]), each at a left boundary.
func findSlack(b []byte, cookie bool) int {
	if cookie {
		return indexAll(b, "xoxd-", func(i int) bool {
			return leftBoundary(b, i) && runLen(b, i+5, cookieSet) >= 20
		})
	}
	xox := indexAll(b, "xox", func(i int) bool {
		if !leftBoundary(b, i) || i+4 >= len(b) || b[i+4] != '-' {
			return false
		}
		return strings.IndexByte("abcdeoprs", b[i+3]) >= 0 && runLen(b, i+5, alnumDashSet) >= 20
	})
	xapp := indexAll(b, "xapp-", func(i int) bool {
		return leftBoundary(b, i) && runLen(b, i+5, alnumDashSet) >= 20
	})
	return minOffset(xox, xapp)
}

func minOffset(a, b int) int {
	switch {
	case a < 0:
		return b
	case b < 0 || a < b:
		return a
	}
	return b
}

// findWebhook matches `hooks.slack.com/services/`, `/workflows/`, or
// `/triggers/` and at least 20 of [A-Za-z0-9/].
func findWebhook(b []byte) int {
	return indexAll(b, "hooks.slack.com/", func(i int) bool {
		rest := b[i+len("hooks.slack.com/"):]
		for _, p := range []string{"services/", "workflows/", "triggers/"} {
			if bytes.HasPrefix(rest, []byte(p)) && runLen(rest, len(p), webhookSet) >= 20 {
				return true
			}
		}
		return false
	})
}

// ---- AWS ----

// findAWS matches `AKIA` or `ASIA` and exactly 16 of [A-Z0-9], with a left
// and a right boundary.
func findAWS(b []byte) int {
	f := func(i int) bool {
		return leftBoundary(b, i) && runLen(b, i+4, upperDigitSet) >= 16 && rightBoundary(b, i+20)
	}
	return minOffset(indexAll(b, "AKIA", f), indexAll(b, "ASIA", f))
}

// ---- GitHub ----

// findGitHub matches `ghp_`, `gho_`, `ghu_`, `ghs_`, or `ghr_` and at least
// 36 of [A-Za-z0-9], or `github_pat_` and at least 82 of [A-Za-z0-9_], at a
// left boundary.
func findGitHub(b []byte) int {
	classic := indexAll(b, "gh", func(i int) bool {
		return leftBoundary(b, i) && i+3 < len(b) && strings.IndexByte("pousr", b[i+2]) >= 0 &&
			b[i+3] == '_' && runLen(b, i+4, alnumSet) >= 36
	})
	pat := indexAll(b, "github_pat_", func(i int) bool {
		return leftBoundary(b, i) && runLen(b, i+11, alnumUnderSet) >= 82
	})
	return minOffset(classic, pat)
}

// ---- model-provider keys ----

// findModelKey matches `sk-`, a lowercase word, `-`, and at least 32 of
// [A-Za-z0-9_-] (`sk-ant-`, `sk-proj-`, …); or `sk-` and at least 40 of
// [A-Za-z0-9]; at a left boundary.
func findModelKey(b []byte) int {
	return indexAll(b, "sk-", func(i int) bool {
		if !leftBoundary(b, i) {
			return false
		}
		j := i + 3
		if w := runLen(b, j, lowerSet); w > 0 && j+w < len(b) && b[j+w] == '-' &&
			runLen(b, j+w+1, b64urlSet) >= 32 {
			return true
		}
		return runLen(b, j, alnumSet) >= 40
	})
}

// ---- JWT ----

// maxJWTHeaderTries bounds the header starts tried per segment run. Inside a
// run of [A-Za-z0-9_-] a left boundary falls only at the run's start and
// after each `-` or `_`, and only a start that can begin a JSON object's
// base64 is tried; the cap keeps a crafted run of `-e-e-e…` from making the
// scan quadratic.
const maxJWTHeaderTries = 16

// findJWT matches three `.`-separated segments of [A-Za-z0-9_-] at a left
// boundary: a header of at least 10 characters that base64url-decodes to a
// JSON object with a string member `alg`, a payload of at least 10, and a
// signature of at least 16.
func findJWT(b []byte) int {
	for from := 0; from < len(b); {
		j := bytes.IndexByte(b[from:], '.')
		if j < 0 {
			return -1
		}
		dot := from + j
		from = dot + 1
		hs := dot
		for hs > 0 && b64urlSet[b[hs-1]] {
			hs--
		}
		if dot-hs < 10 {
			continue
		}
		pl := runLen(b, dot+1, b64urlSet)
		if pl < 10 || dot+1+pl >= len(b) || b[dot+1+pl] != '.' {
			continue
		}
		if runLen(b, dot+2+pl, b64urlSet) < 16 {
			continue
		}
		tries := 0
		for s := hs; dot-s >= 10 && tries < maxJWTHeaderTries; s++ {
			// A JSON object's base64 starts `e` (`{`), or `I`, `C`, or `D`
			// (a leading space, tab or newline, or carriage return). Only
			// those starts count toward the cap.
			if !leftBoundary(b, s) || strings.IndexByte("eICD", b[s]) < 0 {
				continue
			}
			tries++
			if jwtHeader(b[s:dot]) {
				return s
			}
		}
	}
	return -1
}

// jwtHeader reports whether seg base64url-decodes, padding optional, to a
// JSON object with a string member `alg`.
func jwtHeader(seg []byte) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(seg), "="))
	if err != nil {
		return false
	}
	t := bytes.TrimLeft(raw, " \t\r\n")
	if len(t) == 0 || t[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	alg, ok := obj["alg"]
	return ok && len(alg) > 0 && alg[0] == '"'
}

// ---- credential URL ----

// findCredentialURL matches `scheme://user:password@host` anywhere, with a
// non-empty password that is not a placeholder and not, case-insensitive,
// `password`, `pass`, or `secret`.
func findCredentialURL(b []byte) int {
	for from := 0; from < len(b); {
		j := bytes.Index(b[from:], []byte("://"))
		if j < 0 {
			return -1
		}
		i := from + j
		from = i + 1
		k := i
		for k > 0 && schemeSet[b[k-1]] {
			k--
		}
		for k < i && !(b[k] >= 'A' && b[k] <= 'Z' || b[k] >= 'a' && b[k] <= 'z') {
			k++
		}
		if k == i {
			continue
		}
		e := i + 3
		for e < len(b) && !authorityEnd(b[e]) {
			e++
		}
		auth := b[i+3 : e]
		at := bytes.LastIndexByte(auth, '@')
		if at < 0 || at == len(auth)-1 {
			continue
		}
		colon := bytes.IndexByte(auth[:at], ':')
		if colon < 0 {
			continue
		}
		// A user part holding `?`, `#`, `&`, or `=` is a query or fragment
		// that happens to hold `:` and `@`, not userinfo. The password may
		// hold `#`.
		if bytes.ContainsAny(auth[:colon], "?#&=") {
			continue
		}
		if credentialPassword(string(auth[colon+1 : at])) {
			return k
		}
	}
	return -1
}

func authorityEnd(c byte) bool {
	switch c {
	case '/', '"', '\'', '<', '>', '`':
		return true
	}
	return isSpace(c)
}

func credentialPassword(pw string) bool {
	if pw == "" || isPlaceholder(pw) {
		return false
	}
	switch strings.ToLower(pw) {
	case "password", "pass", "secret":
		return false
	}
	return true
}
