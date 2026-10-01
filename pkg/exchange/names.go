package exchange

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxNameBytes is the longest bare name accepted, in bytes of UTF-8.
const MaxNameBytes = 255

// refusedChars are refused anywhere in a bare name on every platform (ADR-012).
const refusedChars = `/\:<>"|?*`

// deviceNames are the Windows device names a stem may not be, compared
// case-insensitively, on every platform so a name valid on one host is valid
// on all of them.
var deviceNames = []string{
	"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
	"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
	"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
	"COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³",
}

// NameError reports a name that fails the bare-name rule. It names the rule.
type NameError struct {
	Name string
	Rule string
}

func (e *NameError) Error() string {
	return fmt.Sprintf("%q is not a bare file name: %s. File parameters take a name inside the exchange directory, not a path", e.Name, e.Rule)
}

// ValidateName applies ADR-012's bare-name rule. It returns a *NameError
// naming the first rule the name breaks, or nil.
func ValidateName(name string) error {
	refuse := func(rule string) error { return &NameError{Name: name, Rule: rule} }

	switch name {
	case "":
		return refuse("it is empty")
	case ".", "..":
		return refuse("it is . or ..")
	}
	if len(name) > MaxNameBytes {
		return refuse(fmt.Sprintf("it is longer than %d bytes", MaxNameBytes))
	}
	if strings.ContainsAny(name, `/\`) {
		return refuse("it is not a single path component (/ and \\ are refused)")
	}
	if !filepath.IsLocal(name) {
		return refuse("it is not a local name")
	}
	for _, r := range name {
		if r < 0x20 {
			return refuse("it contains a control character")
		}
		if strings.ContainsRune(refusedChars, r) {
			return refuse(fmt.Sprintf("it contains %q (refused: : < > \" | ? *)", r))
		}
	}
	if last := name[len(name)-1]; last == '.' || last == ' ' {
		return refuse("it ends in a dot or a space")
	}
	if isDeviceStem(name) {
		return refuse("its stem is a Windows device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9, ...)")
	}
	return nil
}

// isDeviceStem reports whether the part of name before its first dot, with
// trailing spaces removed, is a Windows device name.
func isDeviceStem(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.TrimRight(stem, " ")
	for _, d := range deviceNames {
		if strings.EqualFold(stem, d) {
			return true
		}
	}
	return false
}

// SplitExt splits a name into stem and extension. The extension is what
// filepath.Ext returns unless the only dot is the leading one, so the stem
// of ".env" is ".env" and its extension is empty.
func SplitExt(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	if ext == name {
		return name, ""
	}
	return name[:len(name)-len(ext)], ext
}

// Sanitize turns a Slack-supplied file name into a valid bare name: each
// refused character becomes '_', trailing dots and spaces are dropped, a
// device-name stem gets a leading '_', an over-long name is truncated at a
// rune boundary keeping its extension, and an empty result falls back to
// fallback (the file ID).
func Sanitize(name, fallback string) string {
	name = strings.ToValidUTF8(name, "_")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(refusedChars, r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimRight(b.String(), ". ")
	if name != "" && isDeviceStem(name) {
		name = "_" + name
	}
	if len(name) > MaxNameBytes {
		stem, ext := SplitExt(name)
		if len(ext) >= MaxNameBytes {
			stem, ext = name, ""
		}
		name = truncateRunes(stem, MaxNameBytes-len(ext)) + ext
		name = strings.TrimRight(name, ". ")
	}
	if ValidateName(name) != nil {
		return fallback
	}
	return name
}

// truncateRunes cuts s to at most n bytes without splitting a rune.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Suffixed returns name with " (n)" before its extension, truncating the
// stem when the result would exceed MaxNameBytes.
func Suffixed(name string, n int) string {
	stem, ext := SplitExt(name)
	suffix := fmt.Sprintf(" (%d)", n)
	if room := MaxNameBytes - len(ext) - len(suffix); len(stem) > room {
		stem = truncateRunes(stem, max(room, 0))
	}
	return stem + suffix + ext
}
