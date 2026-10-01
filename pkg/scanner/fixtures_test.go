package scanner

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// Every token-shaped test value is assembled at run time from pieces, so no
// source line holds a string a push-protection scanner would flag. All of
// them are obviously fake: FAKE, zeros, and X runs.

func cat(parts ...string) string { return strings.Join(parts, "") }

var (
	fakeAWS        = cat("AK", "IA", "FAKEFAKEFAKEFAKE")
	fakeSlackBot   = cat("xo", "xb-", "0000000000-0000000000-", "FAKEFAKEFAKEFAKE")
	fakeSlackApp   = cat("xa", "pp-", "1-A0000000000-0000000000000-", "FAKEFAKEFAKE")
	fakeSlackXoxc  = cat("xo", "xc-", "0000000000-0000000000-", "FAKEFAKEFAKEFAKE")
	fakeCookie     = cat("xo", "xd-", "FAKE%2FFAKE%2BFAKE%3DFAKEFAKE")
	fakeWebhook    = cat("https://hooks.slack", ".com/services/", "T00000000/B00000000/", strings.Repeat("X", 24))
	fakeWorkflow   = cat("https://hooks.slack", ".com/workflows/", "T00000000/A00000000/", strings.Repeat("0", 20))
	fakeGitHub     = cat("gh", "p_", strings.Repeat("FAKE", 9))
	fakeGitHubPAT  = cat("github", "_pat_", strings.Repeat("FAKE0", 16), "FA")
	fakeAnthropic  = cat("sk", "-ant-", "api03-", strings.Repeat("FAKE", 8))
	fakeLegacySK   = cat("sk", "-", strings.Repeat("FAKE", 10))
	fakeJWT        = makeJWT(`{"alg":"none","typ":"JWT"}`)
	fakeCredURL    = "postgres://u:p@h/db"
	fakePrivateKey = cat("-----BEGIN ", "RSA PRIVATE", " KEY-----\n",
		strings.Repeat("A", 64), "\n", strings.Repeat("A", 64), "\n-----END RSA PRIVATE KEY-----\n")
)

func makeJWT(header string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(header)) + "." +
		enc.EncodeToString([]byte(`{"sub":"fake-user-0000"}`)) + "." +
		strings.Repeat("FAKESIG0", 3)
}

func text(s string) []Field { return []Field{{Kind: FieldText, Data: []byte(s)}} }

func file(b []byte) []Field { return []Field{{Kind: FieldFileBytes, Data: b}} }

func gz(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zl(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawDeflate compresses b with no zlib or gzip framing.
func rawDeflate(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zlibCINFO frames a deflate stream with a zlib header carrying the given
// window field, as libpng does for small text chunks.
func zlibCINFO(t testing.TB, b []byte, cinfo byte) []byte {
	t.Helper()
	cmf := cinfo<<4 | 8
	flg := byte(0)
	if r := (uint16(cmf) << 8) % 31; r != 0 {
		flg = byte(31 - r)
	}
	if !validZlib(cmf, flg) {
		t.Fatalf("built an invalid zlib header %02x %02x", cmf, flg)
	}
	out := []byte{cmf, flg}
	out = append(out, rawDeflate(t, b)...)
	return binary.BigEndian.AppendUint32(out, adler32.Checksum(b))
}

// withFiller appends compressible text, so the compressors emit Huffman
// blocks rather than a stored block that would carry the secret in the clear.
func withFiller(s string) []byte {
	return []byte(s + strings.Repeat(" lorem ipsum dolor sit amet", 8))
}

// opaque fails the test when compressed bytes still hold the secret in the
// clear, which would make an exemption test pass for the wrong reason.
func opaque(t testing.TB, compressed []byte) []byte {
	t.Helper()
	if bytes.Contains(compressed, []byte(fakeAWS[:8])) {
		t.Fatal("compressed fixture holds the secret in the clear")
	}
	return compressed
}

var (
	zeroBombOnce sync.Once
	zeroBombData []byte
)

// zeroBomb is gzip of 40 MiB of zeros: about 40 KiB that inflates past the
// default budget. Built once; callers must not modify it.
func zeroBomb(t testing.TB) []byte {
	zeroBombOnce.Do(func() { zeroBombData = gz(t, make([]byte, 40<<20)) })
	return zeroBombData
}

func b64(s []byte) string { return base64.StdEncoding.EncodeToString(s) }

// randomBytes is deterministic, so a failure reproduces.
func randomBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(seed))
	_, _ = r.Read(b)
	return b
}

// ---- PNG ----

func pngChunk(typ string, data []byte) []byte {
	var c []byte
	c = binary.BigEndian.AppendUint32(c, uint32(len(data)))
	c = append(c, typ...)
	c = append(c, data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func pngFile(chunks ...[]byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1)
	binary.BigEndian.PutUint32(ihdr[4:], 1)
	ihdr[8], ihdr[9] = 8, 2
	out := append([]byte(nil), pngSignature...)
	out = append(out, pngChunk("IHDR", ihdr)...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, pngChunk("IEND", nil)...)
}

// ---- PDF ----

// pdfStream builds a one-object PDF whose stream has dictionary dict, the
// separator sep between the dictionary and the keyword, and data.
func pdfStream(dict, sep string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj\n")
	b.WriteString(dict)
	b.WriteString(sep)
	b.WriteString("stream\n")
	b.Write(data)
	b.WriteString("\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

// exemptSpans drains newExempt over b: nil when no container parsed, else
// every exempt span in order.
func exemptSpans(b []byte) [][2]int {
	e := newExempt(b)
	if e.kind == exemptNone {
		return nil
	}
	spans := [][2]int{}
	for i := 0; i < len(b); i++ {
		if end, ok := e.covers(i); ok {
			spans = append(spans, [2]int{i, end})
			i = end - 1
		}
	}
	return spans
}

// tinyIDATPNG is a well-formed PNG of at least n bytes made of one-byte IDAT
// chunks, each a zlib header candidate.
func tinyIDATPNG(n int) []byte {
	idat := pngChunk("IDAT", []byte{0x78})
	var b bytes.Buffer
	b.Grow(n + 64)
	b.Write(pngFile())
	b.Truncate(b.Len() - 12) // IEND
	for b.Len() < n {
		b.Write(idat)
	}
	b.Write(pngChunk("IEND", nil))
	return b.Bytes()
}
