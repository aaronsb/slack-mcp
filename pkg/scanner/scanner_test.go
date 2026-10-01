package scanner

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustBlock(t *testing.T, r Result, class Class) {
	t.Helper()
	if !r.Blocked() {
		t.Fatalf("want a %s block, got clean (unscannable=%v, used=%d)", class, r.Unscannable, r.BudgetUsedForLog())
	}
	if got := r.Findings[0].Class; got != class {
		t.Fatalf("want class %s, got %s", class, got)
	}
	if r.Unscannable {
		t.Fatal("a block must never also be unscannable")
	}
}

func mustClean(t *testing.T, r Result) {
	t.Helper()
	if r.Blocked() {
		rec, _ := r.Record()
		t.Fatalf("want clean, got %s (chain %+v)", r.Findings[0].Class, rec.Chain)
	}
	if r.Unscannable {
		t.Fatalf("want scannable, got unscannable (used=%d)", r.BudgetUsedForLog())
	}
}

// ---- pattern classes ----

func TestPatternClassesMatch(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		class Class
	}{
		{"pem rsa", fakePrivateKey, ClassPrivateKey},
		{"pkcs8", cat("-----BEGIN ", "PRIVATE KEY-----\n", strings.Repeat("B", 64), "\n"), ClassPrivateKey},
		{"pem encrypted with headers", cat("-----BEGIN ", "RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,00\n\n",
			strings.Repeat("C", 40), "\n", strings.Repeat("C", 40), "\n"), ClassPrivateKey},
		{"openssh", cat("-----BEGIN ", "OPENSSH PRIVATE KEY-----\r\n", strings.Repeat("D", 70), "\r\n"), ClassPrivateKey},
		{"pgp", cat("-----BEGIN ", "PGP PRIVATE KEY BLOCK-----\nVersion: Fake 1\n\n", strings.Repeat("E", 64)), ClassPrivateKey},
		{"putty", cat("PuTTY-User-", "Key-File-3: ssh-ed25519\nEncryption: none\nComment: fake\nPublic-Lines: 1\nAAAA\nPrivate-Lines: 2\n",
			strings.Repeat("F", 40), "\n", strings.Repeat("F", 40), "\nPrivate-MAC: 00\n"), ClassPrivateKey},
		{"slack bot token", "token " + fakeSlackBot, ClassSlackToken},
		{"slack xoxc", fakeSlackXoxc, ClassSlackToken},
		{"slack app token", fakeSlackApp, ClassSlackToken},
		{"slack cookie", "d=" + fakeCookie, ClassSlackCookie},
		{"slack webhook", fakeWebhook, ClassSlackWebhook},
		{"slack workflow webhook", fakeWorkflow, ClassSlackWebhook},
		{"aws", "key " + fakeAWS + ".", ClassAWSAccessKey},
		{"aws asia", cat("AS", "IA", "FAKEFAKEFAKE0000"), ClassAWSAccessKey},
		{"github classic", fakeGitHub, ClassGitHubToken},
		{"github fine-grained", fakeGitHubPAT, ClassGitHubToken},
		{"model key with word", fakeAnthropic, ClassModelKey},
		{"model key legacy", fakeLegacySK, ClassModelKey},
		{"model key proj", cat("sk", "-proj-", strings.Repeat("FAKE_", 7)), ClassModelKey},
		{"jwt", "Bearer " + fakeJWT, ClassJWT},
		{"jwt glued behind dash", "token-" + fakeJWT, ClassJWT},
		{"jwt header with whitespace", makeJWT(" {\"alg\": \"HS256\"}"), ClassJWT},
		{"credential url", "see " + fakeCredURL, ClassCredentialURL},
		{"credential url empty user", "redis://:FakeFake0@cache:6379", ClassCredentialURL},
		{"env secret", "DB_PASSWORD=Fake#Pass!1a2b3c", ClassEnvSecret},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustBlock(t, Scan(text(c.in), Options{}), c.class)
		})
	}
}

func TestPatternClassesDoNotMatch(t *testing.T) {
	cases := []struct{ name, in string }{
		{"pem header with no body", cat("-----BEGIN ", "RSA PRIVATE KEY-----\n-----END RSA PRIVATE KEY-----\n")},
		{"pem parser code", cat(`if strings.HasPrefix(line, "-----BEGIN `, `PRIVATE KEY-----") {`)},
		{"pem body beyond 512 bytes", cat("-----BEGIN ", "PRIVATE KEY-----\n", strings.Repeat("Comment: x\n", 60), strings.Repeat("A", 64))},
		{"public key", cat("-----BEGIN ", "PUBLIC KEY-----\n", strings.Repeat("A", 64))},
		{"putty without private lines", "PuTTY-User-Key-File-3: ssh-ed25519\nPublic-Lines: 1\nAAAA\n"},
		{"slack token too short", cat("xo", "xb-", "0000000000-0000")},
		{"slack token glued", cat("axo", "xb-", "0000000000-0000000000-FAKE")},
		{"slack token wrong letter", cat("xo", "xz-", "0000000000-0000000000-FAKE")},
		{"webhook too short", cat("hooks.slack", ".com/services/", "T000/B000")},
		{"aws 17 chars", cat("AK", "IA", "FAKEFAKEFAKEFAKE0")},
		{"aws 15 chars", cat("AK", "IA", "FAKEFAKEFAKEFAK")},
		{"aws glued left", cat("XAK", "IA", "FAKEFAKEFAKEFAKE")},
		{"aws lowercase", cat("AK", "IA", "fakefakefakefake")},
		{"github short", cat("gh", "p_", strings.Repeat("F", 35))},
		{"github glued", cat("xgh", "p_", strings.Repeat("F", 40))},
		{"sk short", cat("sk", "-", strings.Repeat("F", 39))},
		{"sk word short", cat("sk", "-ant-", strings.Repeat("F", 31))},
		{"jwt header not json", "abcdefghijk.abcdefghijk.abcdefghijklmnopq"},
		{"jwt header without alg", makeJWT(`{"typ":"JWT","kid":"0"}`)},
		{"jwt alg not string", makeJWT(`{"alg":1,"typ":"JWT"}`)},
		{"jwt short signature", strings.TrimSuffix(fakeJWT, "FAKESIG0FAKESIG0")},
		{"version string", "release v1.2.3.4 build 20260101.0000000001.0000000000000002"},
		{"url without password", "https://user@example.com/path"},
		{"url placeholder password", "postgres://app:${DB_PASSWORD}@db/app"},
		{"url password word", "https://admin:password@host/ and ftp://u:SECRET@h"},
		{"url template password", "https://u:<password>@host"},
		{"prose", "The quick brown fox jumps over the lazy dog. Monkey business, authors, bypasses."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustClean(t, Scan(text(c.in), Options{}))
		})
	}
}

// ---- env secret: ADR-013's tables, verbatim ----

func TestEnvSecretMustNotMatch(t *testing.T) {
	rows := []string{
		"MAX_TOKENS=4096",
		"PASSWORD_MIN_LENGTH=12",
		"TOKEN_TTL=3600",
		"GITHUB_TOKEN=${{ secrets.GITHUB_TOKEN }}",
		"SECRET=changeme",
		"PASSWORD=",
		"API_TOKEN=<your-token-here>",
		"DB_PASSWORD=your_password_here",
		"SECRET_KEY=xxxxxxxxxxxxxxxx",
		"SECRET_NAME=prod-db-credentials",
		"TOKEN_URL=https://login.microsoftonline.com/common/oauth2/v2.0/token",
		"PASSWORD_FILE=/run/secrets/db_password",
		"CSRF_TOKEN_HEADER=X-CSRF-Token",
		"PASSWORD_HASH_ALGORITHM=PBKDF2-SHA256",
		"PASSWORD_HASHER=Argon2PasswordHasher",
		"SSH_PUBLIC_KEY=ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGk3…",
		"DATABASE_URL=postgres://app:${DB_PASSWORD}@db/app",
		"KMS_KEY_ARN=arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
		"AUTH_SCOPES=openid profile email offline_access",
		`SIGNING_KEY_PATH=C:\keys\signing.pem`,
	}
	for _, row := range rows {
		t.Run(row, func(t *testing.T) {
			if envLine([]byte(row)) {
				t.Fatal("env class matched")
			}
			mustClean(t, Scan(text(row), Options{}))
		})
	}
}

func TestEnvSecretMustMatch(t *testing.T) {
	rows := []struct {
		row   string
		class Class
	}{
		{"DB_PASSWORD=Fake#Pass!1a2b3c", ClassEnvSecret},
		{`export API_TOKEN="a1B2c3D4e5F6g7H8i9J0"`, ClassEnvSecret},
		{"SECRET_KEY='9f8e7d6c5b4a39281706f5e4d3c2b1a0'", ClassEnvSecret},
		{"session_token = Zm9vYmFyYmF6cXV4MTIz", ClassEnvSecret},
		{"TOKEN=550e8400-e29b-41d4-a716-446655440000", ClassEnvSecret},
		{"OPENAI_API_KEY=sk-proj-FAKE1a2B3c4D5e6F7g8H9i0J", ClassEnvSecret},
		{"PAYMENTS_KEY=FAKE1a2B3c4D5e6F7g8H", ClassEnvSecret},
		{"DB_PASS=N0t&A&Real&Pass1", ClassEnvSecret},
		{"DATABASE_URL=postgres://u:p@h/db", ClassCredentialURL},
	}
	for _, r := range rows {
		t.Run(r.row, func(t *testing.T) {
			if r.class == ClassEnvSecret && !envLine([]byte(r.row)) {
				t.Fatal("env class did not match")
			}
			mustBlock(t, Scan(text(r.row), Options{}), r.class)
		})
	}
}

// The ADR's known misses: a passphrase of plain words, and a random string
// with no lone letter, are both identifier-shaped.
func TestEnvSecretKnownMisses(t *testing.T) {
	for _, row := range []string{
		"PASSWORD=correcthorsebatterystaple",
		"TOKEN=qwhfkdjsaleirutyzmxn",
	} {
		if envLine([]byte(row)) {
			t.Errorf("%s: matched; the ADR documents it as a miss", row)
		}
	}
}

func TestEnvSecretLineRules(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"  export   API_TOKEN = a1B2c3D4e5F6g7H8i9J0", true},
		{"apiKey=a1B2c3D4e5F6g7H8i9J0", true},               // lower-to-upper step makes KEY a word
		{"MONKEY=a1B2c3D4e5F6g7H8i9J0", false},              // whole words only
		{"AUTHOR=a1B2c3D4e5F6g7H8i9J0", false},              // whole words only
		{"BYPASS=a1B2c3D4e5F6g7H8i9J0", false},              // whole words only
		{"PUB_KEY=a1B2c3D4e5F6g7H8i9J0", false},             // PUB excludes
		{"API_TOKEN=a1B2c3D4e5F6g7H8i9J0 # comment", true},  // unquoted ends at ` #`
		{"API_TOKEN=a1B2c3D4e5F6g7H8i9J0\t# comment", true}, // or a tab and `#`
		{"API_TOKEN=a1B2c3 # D4e5F6g7H8i9J0", false},        // too short once the comment is cut
		{"API_TOKEN=0000000000000000000000", false},         // all digits
		{"API_TOKEN=$(vault read -field=token secret/x)", false},
		{"API_TOKEN=%API_TOKEN_FROM_ENV_VARIABLE%", false},
		{"API_TOKEN=../relative/a1B2c3D4e5F6", false},
		{"API_TOKEN=~/tokens/a1B2c3D4e5F6g7", false},
		{`API_TOKEN=\\server\share\a1B2c3D4`, false},
		{"API_TOKEN=D:/tokens/a1B2c3D4e5F6g7", false},
		{"API_TOKEN=REDACTEDa1B2c3D4e5F6g7", false},
		{"API_TOKEN=****************", false},
		{"API_TOKEN=a1B2c3D4e5F6g7H8i9J0\r", true},
		{"I set API_TOKEN=a1B2c3D4e5F6g7H8i9J0 yesterday", false}, // prose
	}
	for _, c := range cases {
		if got := Scan(text(c.in), Options{}).Blocked(); got != c.want {
			t.Errorf("%q: blocked=%v, want %v", c.in, got, c.want)
		}
	}
}

func TestIdentifierShaped(t *testing.T) {
	cases := map[string]bool{
		"prod-db-credentials":  true,
		"Argon2PasswordHasher": true,
		"CSRFToken":            true,
		"a1B2c3":               false,
		"9f8e":                 false,
		"550e8400":             false,
		"Zm9vYmFyYmF6cXV4MTIz": false,
		"Fake#Pass":            false,
	}
	for v, want := range cases {
		if got := identifierShaped(v); got != want {
			t.Errorf("identifierShaped(%q) = %v, want %v", v, got, want)
		}
	}
}

// In the joined rich_text form each inline element's start counts as a line
// start, so a whole code element inside a sentence is caught.
func TestEnvSecretElementStarts(t *testing.T) {
	joined := "Set API_TOKEN=a1B2c3D4e5F6g7H8i9J0 in the config"
	plain := []Field{{Kind: FieldText, Data: []byte(joined)}}
	mustClean(t, Scan(plain, Options{}))

	rich := []Field{{Kind: FieldText, Index: 2, Data: []byte(joined), ElementStarts: []int{0, 4, 34}}}
	r := Scan(rich, Options{})
	mustBlock(t, r, ClassEnvSecret)
	if r.Findings[0].Field != (FieldRef{Kind: FieldText, Index: 2}) {
		t.Fatalf("field = %+v", r.Findings[0].Field)
	}
}

// A token split by formatting is whole again in the joined form.
func TestJoinedRichTextToken(t *testing.T) {
	joined := "> " + fakeSlackBot
	mustBlock(t, Scan([]Field{{Kind: FieldText, Index: 2, Data: []byte(joined), ElementStarts: []int{0, 2, 6}}}, Options{}), ClassSlackToken)
}

// ---- decoders ----

func TestBase64Aligned(t *testing.T) {
	mustBlock(t, Scan(text("payload: "+b64([]byte("id="+fakeAWS+";"))), Options{}), ClassAWSAccessKey)
}

func TestBase64Misaligned(t *testing.T) {
	enc := b64([]byte("k=" + fakeGitHub))
	for _, prefix := range []string{"a", "ab", "abc", "abcd", "abcde"} {
		t.Run(prefix, func(t *testing.T) {
			mustBlock(t, Scan(text(prefix+enc), Options{}), ClassGitHubToken)
		})
	}
}

func TestBase64URLSafe(t *testing.T) {
	enc := strings.NewReplacer("+", "-", "/", "_").Replace(b64([]byte("\xfb\xff" + fakeAWS + "\xfe")))
	mustBlock(t, Scan(text(enc), Options{}), ClassAWSAccessKey)
}

// A secret straddling a line break of wrapped base64 is found only by
// rejoining the lines.
func TestBase64LineWrapped(t *testing.T) {
	payload := []byte(strings.Repeat("-", 40) + fakeAWS + strings.Repeat(".", 60))
	enc := b64(payload)
	for _, width := range []int{64, 76} {
		for _, indent := range []string{"", "    ", "\t"} {
			var b strings.Builder
			b.WriteString("data: |\n")
			for i := 0; i < len(enc); i += width {
				end := min(i+width, len(enc))
				b.WriteString(indent + enc[i:end] + "\r\n")
			}
			mustBlock(t, Scan(text(b.String()), Options{}), ClassAWSAccessKey)
		}
	}
	// Short lines do not join: each must be at least 40 characters.
	var short strings.Builder
	for i := 0; i < len(enc); i += 32 {
		short.WriteString(enc[i:min(i+32, len(enc))] + "\n")
	}
	if Scan(text(short.String()), Options{}).Blocked() {
		t.Log("short lines: found anyway (a line happened to hold it whole)")
	}
}

func TestHex(t *testing.T) {
	h := hex.EncodeToString([]byte(fakeAWS))
	mustBlock(t, Scan(text(h), Options{}), ClassAWSAccessKey)
	mustBlock(t, Scan(text("f"+h), Options{}), ClassAWSAccessKey) // alignment 1
	mustBlock(t, Scan(text(strings.ToUpper(h)), Options{}), ClassAWSAccessKey)
}

func TestURLEncoding(t *testing.T) {
	mustBlock(t, Scan(text("q=%41"+fakeAWS[1:]+"&x=1"), Options{}), ClassAWSAccessKey)
	mustBlock(t, Scan(text("https%3A%2F%2Fu%3AFakeFake0%40h%2Fdb"), Options{}), ClassCredentialURL)
	// A stray `%` earlier in the span does not cut the span short.
	spans := urlSpans([]byte("a b%zz%41KIA c"))
	if len(spans) != 1 || spans[0] != [2]int{2, 12} {
		t.Fatalf("spans %v", spans)
	}
	// `+` is left as is.
	if got := string(newScan(Options{}).urlDecode([]byte("a+b%20c"))); got != "a+b c" {
		t.Fatalf("decoded %q", got)
	}
}

func TestGzip(t *testing.T) {
	secret := withFiller("config\n" + fakeSlackBot + "\n")
	g := gz(t, secret)
	t.Run("whole", func(t *testing.T) { mustBlock(t, Scan(file(g), Options{}), ClassSlackToken) })
	t.Run("glued behind junk", func(t *testing.T) {
		mustBlock(t, Scan(file(append([]byte("junk\x00\x01"), g...)), Options{}), ClassSlackToken)
	})
	t.Run("magic-number prefix", func(t *testing.T) {
		mustBlock(t, Scan(file(append([]byte{0xff, 0xd8, 0xff, 0xe0}, g...)), Options{}), ClassSlackToken)
	})
	t.Run("missing trailer", func(t *testing.T) {
		mustBlock(t, Scan(file(g[:len(g)-8]), Options{}), ClassSlackToken)
	})
	t.Run("bad checksum", func(t *testing.T) {
		bad := append([]byte(nil), g...)
		bad[len(bad)-6] ^= 0xff
		mustBlock(t, Scan(file(bad), Options{}), ClassSlackToken)
	})
	t.Run("not in text as sent", func(t *testing.T) {
		// Gzip at depth 0 is inflated only in a file's bytes.
		mustClean(t, Scan([]Field{{Kind: FieldText, Data: g}}, Options{}))
	})
	t.Run("behind a glued base64 prefix", func(t *testing.T) {
		mustBlock(t, Scan(text("QUJD"+b64(g)), Options{}), ClassSlackToken)
	})
}

func TestZlibEveryWindow(t *testing.T) {
	secret := []byte("tEXt-ish " + fakeAWS)
	for cinfo := byte(0); cinfo <= 7; cinfo++ {
		z := zlibCINFO(t, secret, cinfo)
		r := Scan(file(append([]byte("prefix"), z...)), Options{})
		mustBlock(t, r, ClassAWSAccessKey)
		rec, _ := r.Record()
		if len(rec.Chain) != 1 || rec.Chain[0].Decoder != DecoderZlib || rec.Chain[0].Offset != len("prefix") {
			t.Fatalf("cinfo %d: chain %+v", cinfo, rec.Chain)
		}
	}
	// A truncated stream still has what it inflated scanned.
	z := zl(t, append([]byte(fakeAWS+" "), randomBytes(4096, 1)...))
	mustBlock(t, Scan(file(z[:len(z)/2]), Options{}), ClassAWSAccessKey)
}

func TestValidZlibHeaders(t *testing.T) {
	n := 0
	for cmf := 0; cmf < 256; cmf++ {
		for flg := 0; flg < 256; flg++ {
			if validZlib(byte(cmf), byte(flg)) {
				n++
			}
		}
	}
	// 8 CMF values, 8 or 9 FLG values each with FCHECK, half with FDICT clear.
	if n < 8*3 || n > 8*5 {
		t.Fatalf("%d valid zlib headers", n)
	}
	for _, h := range [][2]byte{{0x78, 0x01}, {0x78, 0x5e}, {0x78, 0x9c}, {0x78, 0xda}, {0x08, 0x1d}, {0x48, 0x0d}} {
		if !validZlib(h[0], h[1]) {
			t.Errorf("%02x %02x should be valid", h[0], h[1])
		}
	}
	for _, h := range [][2]byte{{0x88, 0x98}, {0x78, 0xbb}, {0x79, 0x9c}} { // CINFO 8, FDICT set, method 9
		if validZlib(h[0], h[1]) {
			t.Errorf("%02x %02x should be invalid", h[0], h[1])
		}
	}
}

// A raw deflate stream with no zlib or gzip header is not inflated (an
// ADR-013 known limit).
func TestRawDeflateNotInflated(t *testing.T) {
	mustClean(t, Scan(file(rawDeflate(t, []byte("raw "+fakeAWS+" raw"))), Options{}))
}

func TestNestedToDepth3(t *testing.T) {
	inner := b64([]byte("x " + fakeAWS + " y"))  // depth 3 is the secret
	mid := gz(t, withFiller("blob:"+inner+"\n")) // depth 2 is base64 text
	outer := "attachment " + b64(mid)            // depth 1 is gzip bytes
	r := Scan(text(outer), Options{})
	mustBlock(t, r, ClassAWSAccessKey)
	rec, ok := r.Record()
	if !ok {
		t.Fatal("no record")
	}
	var chain []Decoder
	for _, s := range rec.Chain {
		chain = append(chain, s.Decoder)
	}
	if !reflect.DeepEqual(chain, []Decoder{DecoderBase64, DecoderGzip, DecoderBase64}) {
		t.Fatalf("chain %v", chain)
	}
	if rec.Chain[0].Offset != len("attachment ") {
		t.Fatalf("first step offset %d", rec.Chain[0].Offset)
	}
}

func TestDepthLimit(t *testing.T) {
	enc := []byte("k " + fakeAWS + " v")
	for i := 0; i < 3; i++ {
		enc = []byte(b64(enc))
	}
	mustBlock(t, Scan(text(string(enc)), Options{}), ClassAWSAccessKey)
	enc = []byte(b64(enc)) // four layers: past depth 3
	mustClean(t, Scan(text(string(enc)), Options{}))
	// MaxDepth tunes it.
	mustBlock(t, Scan(text(string(enc)), Options{MaxDepth: 4}), ClassAWSAccessKey)
}

// ---- budget ----

func TestBudgetExhaustionIsUnscannable(t *testing.T) {
	g := zeroBomb(t)
	r := Scan([]Field{{Kind: FieldText, Data: []byte("hello")}, {Kind: FieldFileBytes, Index: 1, Data: g}}, Options{})
	if !r.Unscannable || r.Blocked() {
		t.Fatalf("want unscannable, got %+v", r)
	}
	if *r.UnscannableField != (FieldRef{Kind: FieldFileBytes, Index: 1}) {
		t.Fatalf("field %+v", *r.UnscannableField)
	}
	if r.BudgetUsedForLog() != DefaultBudget {
		t.Fatalf("used %d", r.BudgetUsedForLog())
	}
	if _, ok := r.Record(); ok {
		t.Fatal("unscannable carries no record")
	}
}

func TestSmallBudgetBase64(t *testing.T) {
	r := Scan(text(b64(randomBytes(4096, 2))), Options{Budget: 1024})
	if !r.Unscannable || r.BudgetUsedForLog() != 1024 {
		t.Fatalf("want unscannable at 1024, got %+v", r)
	}
}

// Gzip nested in gzip expands at each level; a small input reaches the
// budget and fails closed without hanging.
func TestNestedGzipBomb(t *testing.T) {
	bomb := gz(t, gz(t, make([]byte, 64<<20)))
	t.Logf("bomb is %d bytes", len(bomb))
	if len(bomb) > 64<<10 {
		t.Fatalf("bomb not small: %d", len(bomb))
	}
	start := time.Now()
	r := Scan(file(bomb), Options{})
	if !r.Unscannable || r.Blocked() {
		t.Fatalf("want unscannable, got %+v", r)
	}
	d := time.Since(start)
	t.Logf("bomb refused in %v", d)
	if d > 30*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A match found before the budget runs out is a block, never unscannable.
func TestMatchBeforeExhaustionBlocks(t *testing.T) {
	t.Run("partial output scanned", func(t *testing.T) {
		g := gz(t, append([]byte(fakeAWS+"\n"), make([]byte, 40<<20)...))
		mustBlock(t, Scan(file(g), Options{}), ClassAWSAccessKey)
	})
	t.Run("depth 0 before decoding", func(t *testing.T) {
		bomb := zeroBomb(t)
		mustBlock(t, Scan([]Field{
			{Kind: FieldFileBytes, Data: bomb},
			{Kind: FieldFileName, Data: []byte(fakeGitHub + ".txt")},
		}, Options{}), ClassGitHubToken)
	})
	t.Run("earlier span", func(t *testing.T) {
		data := append(gz(t, []byte(fakeAWS)), zeroBomb(t)...)
		mustBlock(t, Scan(file(data), Options{}), ClassAWSAccessKey)
	})
}

// High-entropy content (a JPEG, a zip) pays only for false zlib headers.
func TestRandomBufferLowBudget(t *testing.T) {
	buf := randomBytes(8<<20, 3)
	r := Scan(file(buf), Options{})
	mustClean(t, r)
	pct := 100 * float64(r.BudgetUsedForLog()) / float64(len(buf))
	t.Logf("8 MiB random: budget used %d bytes (%.2f%% of size)", r.BudgetUsedForLog(), pct)
	if pct > 5 {
		t.Fatalf("budget used %.2f%% of size", pct)
	}
}

// ---- containers ----

func TestPNGTextChunks(t *testing.T) {
	idat := pngChunk("IDAT", zl(t, make([]byte, 64)))
	cases := map[string][]byte{
		"tEXt": pngChunk("tEXt", []byte("Comment\x00"+fakeAWS)),
		"zTXt": pngChunk("zTXt", append([]byte("Comment\x00\x00"), zlibCINFO(t, []byte(fakeAWS), 1)...)),
		"iTXt": pngChunk("iTXt", append([]byte("Comment\x00\x01\x00en\x00\x00"), zl(t, withFiller(fakeAWS))...)),
	}
	for name, chunk := range cases {
		t.Run(name, func(t *testing.T) {
			png := pngFile(chunk, idat)
			if exemptSpans(png) == nil {
				t.Fatal("well-formed PNG did not parse")
			}
			mustBlock(t, Scan(file(png), Options{}), ClassAWSAccessKey)
		})
	}
}

// A secret compressed into the pixel chunks of a well-formed PNG is not
// found: ADR-013's documented limit (Exempt pixel data).
func TestPNGIDATExempt(t *testing.T) {
	pixels := opaque(t, zl(t, withFiller("\x00"+fakeAWS+"\n")))
	png := pngFile(pngChunk("IDAT", pixels))
	mustClean(t, Scan(file(png), Options{}))

	t.Run("fdAT", func(t *testing.T) {
		mustClean(t, Scan(file(pngFile(pngChunk("fdAT", append([]byte{0, 0, 0, 1}, pixels...)))), Options{}))
	})
	t.Run("chunks between pixel chunks are inflated", func(t *testing.T) {
		text := pngChunk("zTXt", append([]byte("Comment\x00\x00"), zl(t, withFiller(fakeAWS))...))
		mixed := pngFile(pngChunk("IDAT", pixels), text, pngChunk("IDAT", pixels))
		if n := len(exemptSpans(mixed)); n != 2 {
			t.Fatalf("%d spans", n)
		}
		mustBlock(t, Scan(file(mixed), Options{}), ClassAWSAccessKey)
	})
	t.Run("broken CRC exempts nothing", func(t *testing.T) {
		bad := append([]byte(nil), png...)
		bad[len(pngSignature)+8+13] ^= 0xff // IHDR's CRC
		mustBlock(t, Scan(file(bad), Options{}), ClassAWSAccessKey)
	})
	t.Run("no IEND exempts nothing", func(t *testing.T) {
		mustBlock(t, Scan(file(png[:len(png)-12]), Options{}), ClassAWSAccessKey)
	})
	t.Run("bytes after IEND are scanned", func(t *testing.T) {
		mustBlock(t, Scan(file(append(pngFile(), zl(t, []byte(fakeAWS))...)), Options{}), ClassAWSAccessKey)
	})
	t.Run("signature alone exempts nothing", func(t *testing.T) {
		mustBlock(t, Scan(file(append(append([]byte(nil), pngSignature...), pixels...)), Options{}), ClassAWSAccessKey)
	})
}

func TestPDFTextStream(t *testing.T) {
	content := zl(t, []byte("BT /F1 12 Tf 72 712 Td ("+fakeAWS+") Tj ET"))
	pdf := pdfStream(fmt.Sprintf("<< /Length %d /Filter /FlateDecode >>", len(content)), "\n", content)
	mustBlock(t, Scan(file(pdf), Options{}), ClassAWSAccessKey)
}

func TestPDFImageStreamExempt(t *testing.T) {
	pixels := opaque(t, zl(t, withFiller(fakeAWS+"\n")))
	cases := []struct{ name, dict, sep string }{
		{"subtype image", "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /Filter /FlateDecode >>", "\n"},
		{"no whitespace", "<</Type/XObject/Subtype/Image/Filter/FlateDecode>>", ""},
		{"spaces before keyword", "<< /Subtype /Image >>", "  "},
		{"filter name", "<< /Filter /DCTDecode >>", "\r\n"},
		{"filter array", "<< /Filter [/FlateDecode /JBIG2Decode] >>", "\n"},
		{"nested decode parms", "<< /Subtype/Image /DecodeParms << /Predictor 15 >> >>", "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pdf := pdfStream(c.dict, c.sep, pixels)
			if len(exemptSpans(pdf)) != 1 {
				t.Fatalf("spans %v", exemptSpans(pdf))
			}
			mustClean(t, Scan(file(pdf), Options{}))
		})
	}
}

// A dictionary the scanner cannot read as ADR-013 specifies exempts nothing,
// whatever it says: the stream is inflated.
func TestPDFUnparsableDictionaryInflated(t *testing.T) {
	pixels := opaque(t, zl(t, withFiller(fakeAWS+"\n")))
	cases := []struct{ name, dict, sep string }{
		{"junk between dictionary and keyword", "<< /Subtype /Image >> junk", "\n"},
		{"dictionary beyond 4 KiB", "<< /Subtype /Image /Pad (" + strings.Repeat("p", 5000) + ") >>", "\n"},
		{"unbalanced dictionary", "/Subtype /Image >>", "\n"},
		{"nested subtype only", "<< /Resources << /Subtype /Image >> >>", "\n"},
		{"image subtype in a string", "<< /Note (/Subtype /Image) >>", "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustBlock(t, Scan(file(pdfStream(c.dict, c.sep, pixels)), Options{}), ClassAWSAccessKey)
		})
	}
	t.Run("no endstream", func(t *testing.T) {
		pdf := []byte("%PDF-1.7\n1 0 obj\n<< /Subtype /Image >>\nstream\n")
		pdf = append(pdf, pixels...)
		mustBlock(t, Scan(file(pdf), Options{}), ClassAWSAccessKey)
	})
	t.Run("no PDF header", func(t *testing.T) {
		pdf := pdfStream("<< /Subtype /Image >>", "\n", pixels)
		pdf = append(bytes.Repeat([]byte(" "), 1100), pdf[len("%PDF-1.7"):]...)
		mustBlock(t, Scan(file(pdf), Options{}), ClassAWSAccessKey)
	})
	t.Run("keyword in a comment", func(t *testing.T) {
		pdf := []byte("%PDF-1.7\n<< /Subtype /Image >>\n%stream\n")
		pdf = append(pdf, pixels...)
		pdf = append(pdf, "\nendstream\n"...)
		mustBlock(t, Scan(file(pdf), Options{}), ClassAWSAccessKey)
	})
}

// ---- ordering, determinism, and what a result carries ----

func TestFieldOrder(t *testing.T) {
	fields := []Field{
		{Kind: FieldFileBytes, Index: 0, Data: []byte(fakeAWS)},
		{Kind: FieldFileName, Index: 0, Data: []byte(fakeAnthropic)},
		{Kind: FieldText, Index: 1, Data: []byte(fakeGitHub)},
	}
	r := Scan(fields, Options{})
	mustBlock(t, r, ClassGitHubToken)
	if r.Findings[0].Field != (FieldRef{Kind: FieldText, Index: 1}) {
		t.Fatalf("field %+v", r.Findings[0].Field)
	}
	// Earliest offset wins within a buffer.
	mustBlock(t, Scan(text(fakeAWS+" "+fakeGitHub), Options{}), ClassAWSAccessKey)
}

// Depth 0 everywhere is matched before anything is decoded.
func TestBreadthFirst(t *testing.T) {
	fields := []Field{
		{Kind: FieldText, Data: []byte(b64([]byte(fakeAWS)))},
		{Kind: FieldFileBytes, Data: []byte(fakeGitHub)},
	}
	mustBlock(t, Scan(fields, Options{}), ClassGitHubToken)
}

func TestDeterministic(t *testing.T) {
	fields := []Field{
		{Kind: FieldText, Data: []byte("hello " + b64(gz(t, withFiller("x "+fakeJWT+"\n"))))},
		{Kind: FieldFileBytes, Data: append(randomBytes(1<<20, 4), gz(t, withFiller(fakeAWS))...)},
	}
	a, b := Scan(fields, Options{}), Scan(fields, Options{})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("results differ:\n%+v\n%+v", a, b)
	}
	// The JWT sits at depth 2, the AWS key at depth 1: breadth-first finds
	// the shallower one, though its field comes later.
	mustBlock(t, a, ClassAWSAccessKey)
}

// The Finding is the class and the field; the Record adds the offset and
// chain for the quarantine file. Neither holds the matched value.
func TestResultCarriesNoValue(t *testing.T) {
	ft := reflect.TypeOf(Finding{})
	if ft.NumField() != 2 {
		t.Fatalf("Finding grew a field: %v", ft)
	}
	if reflect.TypeOf(FieldRef{}).NumField() != 2 {
		t.Fatal("FieldRef grew a field")
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(Record{}), reflect.TypeOf(Step{}), ft} {
		for i := 0; i < typ.NumField(); i++ {
			if k := typ.Field(i).Type.Kind(); k == reflect.String && typ.Field(i).Type.Name() == "string" {
				t.Errorf("%s.%s is a raw string", typ.Name(), typ.Field(i).Name)
			}
			if typ.Field(i).Type == reflect.TypeOf([]byte(nil)) {
				t.Errorf("%s.%s holds bytes", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}
