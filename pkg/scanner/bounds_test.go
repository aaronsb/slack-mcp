package scanner

import (
	"bytes"
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Termination, time, and memory bounds on adversarial shapes, and the
// review's follow-up rules (#127).

// within fails the test when fn takes longer than limit (ten times longer
// under the race detector) and logs the time either way.
func within(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	if raceEnabled {
		limit *= 10
	}
	start := time.Now()
	fn()
	d := time.Since(start)
	t.Logf("%v (limit %v)", d, limit)
	if d > limit {
		t.Fatalf("took %v, limit %v", d, limit)
	}
}

// ---- B1: the PDF walk is linear ----

func TestPDFWalkLinear(t *testing.T) {
	cases := map[string][]byte{
		"stream without endstream": append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("stream\n"), 1<<20/7)...),
		"stream closed at once":    append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte(">>stream\n endstream\n"), 4<<20/20)...),
		"image streams closed at once": append([]byte("%PDF-1.7\n"),
			bytes.Repeat([]byte("<</Subtype/Image>>stream\n endstream\n"), 4<<20/36)...),
	}
	for name, pdf := range cases {
		t.Run(name, func(t *testing.T) {
			within(t, 100*time.Millisecond, func() { exemptSpans(pdf) })
			within(t, time.Second, func() { Scan(file(pdf), Options{}) })
		})
	}
}

// The same shapes behind gzip are refused or finish fast.
func TestPDFWalkLinearBehindGzip(t *testing.T) {
	for name, pdf := range map[string][]byte{
		"stream": append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("stream\n"), 24<<20/7)...),
		"endstream": append([]byte("%PDF-1.7\n"),
			bytes.Repeat([]byte(">>stream\n endstream\n"), 24<<20/20)...),
	} {
		t.Run(name, func(t *testing.T) {
			g := gz(t, pdf)
			t.Logf("bomb is %d bytes", len(g))
			within(t, 2*time.Second, func() {
				if r := Scan(file(g), Options{}); r.Blocked() {
					t.Fatal("blocked")
				}
			})
		})
	}
}

// ---- B2: memory follows the budget, not the input's shape ----

// allocated reports the bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestMemoryFollowsBudget(t *testing.T) {
	const size = 16 << 20
	cases := map[string][]byte{
		"url escape every 4 bytes":     bytes.Repeat([]byte("%41 "), size/4),
		"zlib header every 2 bytes":    bytes.Repeat([]byte("x\x01"), size/2),
		"short base64 run every 25":    bytes.Repeat([]byte("QUFBQUFBQUFBQUFBQUFBQUFB "), size/25),
		"hex run every 41":             bytes.Repeat([]byte(strings.Repeat("ab", 20)+" "), size/41),
		"tiny gzip members throughout": bytes.Repeat(gz(t, []byte("a")), size/21),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			var r Result
			alloc := allocated(func() { r = Scan(file(in), Options{}) })
			t.Logf("allocated %d MiB, used %d, unscannable=%v", alloc>>20, r.BudgetUsedForLog(), r.Unscannable)
			// Everything the scan holds is charged at least childOverhead
			// bytes per buffer, so allocation stays a small multiple of
			// the budget whatever the input's shape.
			if alloc > 4*uint64(DefaultBudget) {
				t.Fatalf("allocated %d MiB", alloc>>20)
			}
		})
	}
}

// A PNG of one-byte IDAT chunks exempts every chunk, yet the spans are
// found as the inflate cursor reaches them, so the scan allocates nothing
// in proportion to the chunk count (#127 re-review).
func TestPNGTinyIDATChunksBounded(t *testing.T) {
	png := tinyIDATPNG(16 << 20)
	var r Result
	alloc := allocated(func() { r = Scan(file(png), Options{}) })
	t.Logf("allocated %d KiB, used %d", alloc>>10, r.BudgetUsedForLog())
	mustClean(t, r)
	if r.BudgetUsedForLog() != 0 {
		t.Fatalf("pixel data was inflated: used %d", r.BudgetUsedForLog())
	}
	if alloc > 1<<20 {
		t.Fatalf("allocated %d KiB", alloc>>10)
	}
}

// Each child costs at least childOverhead, so a flood of tiny outputs is
// refused rather than held.
func TestTinyChildrenExhaustBudget(t *testing.T) {
	r := Scan(file(bytes.Repeat([]byte("%41 "), 1<<20)), Options{Budget: 1 << 20})
	if !r.Unscannable {
		t.Fatalf("want unscannable, got %+v", r)
	}
}

// Inflate attempts charge the input they consume: false zlib headers that
// inflate nothing still exhaust a small budget.
func TestInflateChargesInput(t *testing.T) {
	r := Scan(file(bytes.Repeat([]byte("x\x01"), 10000)), Options{Budget: 8192})
	if !r.Unscannable {
		t.Fatalf("want unscannable, got used=%d", r.BudgetUsedForLog())
	}
	// The same bytes in text as sent are not inflated, and cost nothing.
	mustClean(t, Scan([]Field{{Kind: FieldText, Data: bytes.Repeat([]byte("x\x01"), 10000)}}, Options{Budget: 8192}))
}

// ---- B3: the env class is linear in elements ----

func TestEnvElementsLinear(t *testing.T) {
	starts := make([]int, 10000)
	for i := range starts {
		starts[i] = i
	}
	for name, data := range map[string]string{
		"letters":         strings.Repeat("a", 10000),
		"name then equal": strings.Repeat("A", 9999) + "=",
		"assignments":     strings.Repeat("K=", 5000),
	} {
		t.Run(name, func(t *testing.T) {
			f := []Field{{Kind: FieldText, Data: []byte(data), ElementStarts: starts}}
			within(t, 20*time.Millisecond, func() { findEnvSecret(f[0].Data, starts) })
			within(t, 200*time.Millisecond, func() { Scan(f, Options{}) })
		})
	}
}

// An element start at a line start is judged on its element too.
func TestEnvElementAtLineStart(t *testing.T) {
	joined := "API_TOKEN=a1B2c3D4e5F6g7H8i9J0 in the config"
	mustClean(t, Scan(text(joined), Options{}))
	mustBlock(t, Scan([]Field{{Kind: FieldText, Data: []byte(joined), ElementStarts: []int{0, 30}}}, Options{}), ClassEnvSecret)
}

// Low entropy alone rejects this value: it is long enough, has no
// whitespace, and is not identifier-shaped.
func TestEnvEntropyOnly(t *testing.T) {
	v := "a1a1a1a1a1a1a1a1"
	if identifierShaped(v) || len(v) < 16 || entropy(v) >= 3.0 {
		t.Fatal("fixture no longer isolates entropy")
	}
	if envLine([]byte("API_TOKEN=" + v)) {
		t.Fatal("matched")
	}
}

func TestEntropyDeterministic(t *testing.T) {
	v := "Fake#Pass!1a2b3cZm9vYmFy"
	first := entropy(v)
	for i := 0; i < 100; i++ {
		if entropy(v) != first {
			t.Fatal("entropy varies between calls")
		}
	}
}

// ---- W1: JSON-escaped PEM ----

func TestJSONEscapedPEM(t *testing.T) {
	body := strings.Repeat("FAKEFAKEFAKEFAKE", 4)
	key := cat(`{"type": "service_account", "private_key": "-----BEGIN `, `PRIVATE KEY-----\n`,
		body, `\n`, body, `\n-----END PRIVATE KEY-----\n", "client_email": "fake@example.iam"}`)
	mustBlock(t, Scan(text(key), Options{}), ClassPrivateKey)
	mustBlock(t, Scan(file([]byte(key)), Options{}), ClassPrivateKey)
	crlf := strings.ReplaceAll(key, `\n`, `\r\n`)
	mustBlock(t, Scan(file([]byte(crlf)), Options{}), ClassPrivateKey)
	// A header with an escaped line break and no body still does not match.
	mustClean(t, Scan(text(cat(`"-----BEGIN `, `PRIVATE KEY-----\n-----END PRIVATE KEY-----\n"`)), Options{}))
}

// ---- W2: small PuTTY keys ----

func puttyKey(alg string, privateLines int, body ...string) string {
	return cat("PuTTY-User-", "Key-File-3: ", alg, "\nEncryption: none\nComment: fake\nPublic-Lines: 1\nAAAAFAKE\n",
		"Private-Lines: ", string(rune('0'+privateLines)), "\n", strings.Join(body, "\n"), "\nPrivate-MAC: fake\n")
}

func TestPuTTYSmallKeys(t *testing.T) {
	ed := "AAAAIF" + strings.Repeat("FAKE", 9) + "Fk=" // 44 characters, an Ed25519 private line
	ec := "AAAAIQ" + strings.Repeat("FAKE", 10) + "FA" // 48 characters, an ECDSA P-256 private line
	mustBlock(t, Scan(text(puttyKey("ssh-ed25519", 1, ed)), Options{}), ClassPrivateKey)
	mustBlock(t, Scan(text(puttyKey("ecdsa-sha2-nistp256", 1, ec)), Options{}), ClassPrivateKey)
	// Characters across the declared lines add up.
	mustBlock(t, Scan(text(puttyKey("ssh-rsa", 2, strings.Repeat("F", 20), strings.Repeat("F", 20))), Options{}), ClassPrivateKey)
	// Below the minimum, or past the declared lines, does not match.
	mustClean(t, Scan(text(puttyKey("ssh-ed25519", 1, strings.Repeat("F", 39))), Options{}))
	mustClean(t, Scan(text(puttyKey("ssh-rsa", 1, strings.Repeat("F", 20), strings.Repeat("F", 20))), Options{}))
	mustClean(t, Scan(text(puttyKey("ssh-ed25519", 0, ed)), Options{}))
}

// A run of PuTTY headers is not searched quadratically.
func TestPuTTYHeadersLinear(t *testing.T) {
	in := bytes.Repeat([]byte("PuTTY-User-Key-File-"), 1<<20/20)
	within(t, 100*time.Millisecond, func() { findPrivateKey(in) })
}

// Headers sharing one `Private-Lines:` read its body once, however many
// lines it declares (#127 re-review).
func TestPuTTYBodyReadOnce(t *testing.T) {
	var b bytes.Buffer
	for b.Len() < 4000 {
		b.WriteString("PuTTY-User-Key-File-")
	}
	b.WriteString("Private-Lines: 999999\n")
	b.Write(bytes.Repeat([]byte("\n"), 1<<20))
	in := b.Bytes()
	within(t, 20*time.Millisecond, func() {
		if findPrivateKey(in) >= 0 {
			t.Fatal("matched")
		}
	})
}

// ---- W3: credential URLs in queries and fragments ----

func TestCredentialURLQueryAndFragment(t *testing.T) {
	for _, in := range []string{
		"https://calendar.example.com?src=team:roster@group.calendar.example.com",
		"https://meet.example.com#room=standup:daily@team",
		"https://example.com?a=1&b=x:y@z",
	} {
		mustClean(t, Scan(text(in), Options{}))
	}
	// A password may hold `#`.
	mustBlock(t, Scan(text("postgres://app:Fa#ke0@db/app"), Options{}), ClassCredentialURL)
	// `&` and `=` are legal in userinfo, also behind an env name, which
	// leaves URLs to this class.
	mustBlock(t, Scan(text("postgres://svc=ro:Fa8ke0Pw@db/app"), Options{}), ClassCredentialURL)
	mustBlock(t, Scan(text("DATABASE_URL=postgres://a&b:Fa8ke0Pw@db/app"), Options{}), ClassCredentialURL)
}

// ---- W4: JWT starts ----

func TestJWTStarts(t *testing.T) {
	mustBlock(t, Scan(text(strings.Repeat("x-", 17)+fakeJWT), Options{}), ClassJWT)
	// Prefixed starts that could begin a header do not spend the cap.
	mustBlock(t, Scan(text(strings.Repeat("e-", 17)+fakeJWT), Options{}), ClassJWT)
	mustBlock(t, Scan(text(strings.Repeat("ew-", 40)+fakeJWT), Options{}), ClassJWT)
	// Not at a left boundary.
	mustClean(t, Scan(text("x"+fakeJWT), Options{}))
	mustClean(t, Scan(text("9"+fakeJWT), Options{}))
}

// The segment minimums: the smallest possible header (`{"alg":""}`, 14
// characters), a payload of 10, and a signature of 16 match; one fewer in the
// payload or signature does not.
func TestJWTMinimums(t *testing.T) {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":""}`))
	if len(h) != 14 {
		t.Fatalf("header %d", len(h))
	}
	jwt := func(payload, sig int) string {
		return h + "." + strings.Repeat("A", payload) + "." + strings.Repeat("B", sig)
	}
	mustBlock(t, Scan(text(jwt(10, 16)), Options{}), ClassJWT)
	mustClean(t, Scan(text(jwt(9, 16)), Options{}))
	mustClean(t, Scan(text(jwt(10, 15)), Options{}))
}

// ---- PDF token rules ----

// pdfSafeData is zlib data holding the secret with no `(`, `)`, `\`, or line
// breaks, so it can sit inside a literal string or a comment line.
func pdfSafeData(t *testing.T) []byte {
	for n := 0; n < 200; n++ {
		// No spaces: a fixed-Huffman space lands on a line break.
		d := opaque(t, zl(t, []byte(fakeAWS+"|"+strings.Repeat("kq-", 30+n))))
		if !bytes.ContainsAny(d, "()\\\r\n") {
			return d
		}
	}
	t.Fatal("no safe fixture")
	return nil
}

func TestPDFTokenRules(t *testing.T) {
	data := pdfSafeData(t)
	pdf := func(parts ...string) []byte {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		b.WriteString(parts[0])
		b.Write(data)
		b.WriteString(parts[1])
		return b.Bytes()
	}
	// Control: the token parses and exempts.
	mustClean(t, Scan(file(pdf("<< /Subtype /Image >>stream\n", "\nendstream\n")), Options{}))
	// A keyword mid-line, not after `>>`, is not a token.
	mustBlock(t, Scan(file(pdf("<< /Subtype /Image >>\nfoo stream\n", "\nendstream\n")), Options{}), ClassAWSAccessKey)
	// A keyword inside a literal string is not a token.
	mustBlock(t, Scan(file(pdf("(<< /Subtype /Image >>stream\n", "\nendstream)\n")), Options{}), ClassAWSAccessKey)
	// A keyword on a comment line is not a token.
	mustBlock(t, Scan(file(pdf("% << /Subtype /Image >>stream\n", "\nendstream\n")), Options{}), ClassAWSAccessKey)
}
