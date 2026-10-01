package exchange

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateNameRefuses(t *testing.T) {
	for _, name := range []string{
		"", ".", "..",
		"../x", "x/..", "/abs", `C:\x`, `a\b`, "a/b",
		"a:b", "file.txt:stream",
		"CON", "con", "NUL.txt", "nul.tar.gz", "COM1", "lpt9.log", "CONIN$", "conout$.txt",
		"COM¹", "lpt³.txt", "CON .txt", "AUX ",
		"name.", "name ", "name..",
		"a\x00b", "a\x01b", "tab\there", "line\nbreak", "\x1f",
		"a<b", "a>b", `a"b`, "a|b", "a?b", "a*b",
		strings.Repeat("a", 256),
		strings.Repeat("é", 128), // 256 bytes
	} {
		err := ValidateName(name)
		var ne *NameError
		if !errors.As(err, &ne) {
			t.Errorf("ValidateName(%q) = %v, want a NameError", name, err)
		}
	}
}

func TestValidateNameAccepts(t *testing.T) {
	for _, name := range []string{
		"report.pdf", "notes..txt", ".env", "README", "archive.tar.gz",
		"CONSOLE.txt", "COM10", "nullable.go", "my file (1).txt", "Ünïcödé.md",
		strings.Repeat("a", 255),
	} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
}

func TestSanitize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"a/b:c.txt", "a_b_c.txt"},
		{`..\..\evil`, `.._.._evil`},
		{"q?<>|*\".txt", "q______.txt"},
		{"tab\tname.txt", "tab_name.txt"},
		{"name. . ", "name"},
		{"CON.txt", "_CON.txt"},
		{"nul", "_nul"},
		{"", "F123"},
		{"...", "F123"},
		{"  ", "F123"},
		{".env", ".env"},
		{"invoice\u202Efdp.exe", "invoice_fdp.exe"},
		{"a\u200Db.txt", "a_b.txt"},
		{"x\u0085y\u009F.txt", "x_y_.txt"},
		{"del\x7f.txt", "del_.txt"},
	} {
		if got := Sanitize(tc.in, "F123"); got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	long := strings.Repeat("é", 200) + ".pdf"
	got := Sanitize(long, "F1")
	if len(got) > MaxNameBytes || !strings.HasSuffix(got, ".pdf") || !utf8.ValidString(got) {
		t.Fatalf("long name sanitized to %q (%d bytes)", got, len(got))
	}
	if err := ValidateName(got); err != nil {
		t.Fatalf("sanitized long name invalid: %v", err)
	}
}

func TestSuffixed(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"report.pdf", "report (1).pdf"},
		{"README", "README (1)"},
		{".env", ".env (1)"},
		{"archive.tar.gz", "archive.tar (1).gz"},
	} {
		if got := Suffixed(tc.in, 1); got != tc.want {
			t.Errorf("Suffixed(%q, 1) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("x", 251) + ".pdf"
	got := Suffixed(long, 42)
	if len(got) > MaxNameBytes || !strings.HasSuffix(got, " (42).pdf") {
		t.Fatalf("Suffixed(long) = %q (%d bytes)", got, len(got))
	}
}
