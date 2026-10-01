package scanner

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// exempt yields the pixel data the scanner has parsed in a well-formed
// container, which is exempt from inflate attempts (ADR-013, Pixel data).
// Nothing else is exempt: a magic number alone exempts nothing. Matching and
// the base64, hex, and URL decoders still read every byte. Spans are found as
// the inflate cursor reaches them, one ahead, never listed up front.
type exempt struct {
	b    []byte
	kind int
	p    int // where the container walk resumes
	// floor is the end of the previous PDF stream's data. A dictionary is
	// never looked for behind it, so each byte is walked back over at most
	// once.
	floor int
	cur   [2]int
	have  bool
}

const (
	exemptNone = iota
	exemptPNG
	exemptPDF
)

func newExempt(b []byte) *exempt {
	e := &exempt{b: b}
	switch {
	case pngValid(b):
		e.kind, e.p = exemptPNG, len(pngSignature)
	case pdfHeader(b):
		e.kind = exemptPDF
	}
	return e
}

// covers reports whether offset i lies in an exempt span and, if so, where
// that span ends. Offsets must not decrease from one call to the next.
func (e *exempt) covers(i int) (int, bool) {
	for e.kind != exemptNone && (!e.have || e.cur[1] <= i) {
		switch e.kind {
		case exemptPNG:
			e.cur, e.have = e.nextPNG()
		case exemptPDF:
			e.cur, e.have = e.nextPDF()
		}
		if !e.have {
			e.kind = exemptNone
		}
	}
	if e.have && e.cur[0] <= i && i < e.cur[1] {
		return e.cur[1], true
	}
	return 0, false
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// pngValid walks a PNG chunk by chunk and reports whether every chunk up to
// and including IEND has a length inside the buffer, an ASCII-letter type,
// and a CRC-32 that verifies. Only then is the data of each IDAT and fdAT
// chunk exempt; a walk that fails anywhere exempts nothing. Bytes after IEND
// are not part of the walk. The walk records nothing.
func pngValid(b []byte) bool {
	if !bytes.HasPrefix(b, pngSignature) {
		return false
	}
	p := len(pngSignature)
	for {
		if len(b)-p < 12 {
			return false
		}
		n := int64(binary.BigEndian.Uint32(b[p:]))
		if n > int64(len(b)-p-12) {
			return false
		}
		typ := b[p+4 : p+8]
		for _, c := range typ {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				return false
			}
		}
		end := p + 8 + int(n)
		if crc32.ChecksumIEEE(b[p+4:end]) != binary.BigEndian.Uint32(b[end:]) {
			return false
		}
		if string(typ) == "IEND" {
			return true
		}
		p = end + 4
	}
}

// nextPNG returns the data of the next non-empty IDAT or fdAT chunk of a PNG
// pngValid has accepted.
func (e *exempt) nextPNG() ([2]int, bool) {
	b := e.b
	for {
		p := e.p
		end := p + 8 + int(binary.BigEndian.Uint32(b[p:]))
		e.p = end + 4
		switch string(b[p+4 : p+8]) {
		case "IDAT", "fdAT":
			if end > p+8 {
				return [2]int{p + 8, end}, true
			}
		case "IEND":
			return [2]int{}, false
		}
	}
}

func isPDFSpace(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isPDFDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return isPDFSpace(c)
}

// pdfHeader reports whether `%PDF-` is in the buffer's first 1024 bytes.
func pdfHeader(b []byte) bool {
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

// nextPDF reads a PDF on from where the last call stopped for the next
// stream whose dictionary marks it as an image, and returns its data.
//
// A `stream` keyword is a token when it starts a line or follows `>>`, with
// or without whitespace between, outside a literal string and outside a
// comment. Its dictionary is the `<<…>>` that ends, with only whitespace
// after it, at the keyword, found within 4 KiB before it, nesting counted.
// The data runs from the end of the keyword's line to the next `endstream`
// at a line start or after whitespace.
func (e *exempt) nextPDF() ([2]int, bool) {
	b := e.b
	for i := e.p; i < len(b); {
		switch c := b[i]; {
		case c == '%':
			i = skipLine(b, i)
		case c == '(':
			i = skipLiteral(b, i)
		case c == 's' && bytes.HasPrefix(b[i:], []byte("stream")) && streamToken(b, i):
			data, ok := streamDataStart(b, i+6)
			if !ok {
				i += 6
				continue
			}
			end := findEndstream(b, data)
			if end < 0 {
				// No endstream follows anywhere, so no later stream has
				// data either.
				return [2]int{}, false
			}
			dict, ok := dictBefore(b, i, e.floor)
			e.p = end + len("endstream")
			e.floor = e.p
			if ok && imageDict(dict) {
				return [2]int{data, end}, true
			}
			i = e.p
		default:
			i++
		}
	}
	return [2]int{}, false
}

// skipLine returns the index after the line ending that ends the line at i.
func skipLine(b []byte, i int) int {
	for i < len(b) && b[i] != '\n' && b[i] != '\r' {
		i++
	}
	return i + 1
}

// skipLiteral skips a literal string starting at its `(`, nesting counted
// and backslash escapes honored.
func skipLiteral(b []byte, i int) int {
	depth := 0
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return i
}

// streamToken reports whether the `stream` at i starts a line or follows
// `>>`, possibly after whitespace.
func streamToken(b []byte, i int) bool {
	if i == 0 || b[i-1] == '\n' || b[i-1] == '\r' {
		return true
	}
	k := i
	for k > 0 && isPDFSpace(b[k-1]) {
		k--
	}
	return k >= 2 && b[k-2] == '>' && b[k-1] == '>'
}

// streamDataStart returns where a stream's data starts: after the line
// ending that ends the keyword's line (spaces and tabs before it allowed).
func streamDataStart(b []byte, i int) (int, bool) {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	switch {
	case i+1 < len(b) && b[i] == '\r' && b[i+1] == '\n':
		return i + 2, true
	case i < len(b) && (b[i] == '\n' || b[i] == '\r'):
		return i + 1, true
	}
	return 0, false
}

// findEndstream finds the next `endstream` at or after i that starts a line
// or follows whitespace.
func findEndstream(b []byte, i int) int {
	kw := []byte("endstream")
	for i < len(b) {
		j := bytes.Index(b[i:], kw)
		if j < 0 {
			return -1
		}
		p := i + j
		if p > 0 && isPDFSpace(b[p-1]) {
			return p
		}
		i = p + 1
	}
	return -1
}

// dictBefore finds the `<<…>>` that ends, with only whitespace after it, at
// the keyword at kw, within 4 KiB before it and after floor, nesting counted.
func dictBefore(b []byte, kw, floor int) ([]byte, bool) {
	k := kw
	for k > floor && isPDFSpace(b[k-1]) {
		k--
	}
	if k < 2 || b[k-2] != '>' || b[k-1] != '>' {
		return nil, false
	}
	lo := max(kw-4096, floor)
	depth := 1
	for p := k - 3; p-1 >= lo; {
		switch {
		case b[p-1] == '>' && b[p] == '>':
			depth++
			p -= 2
		case b[p-1] == '<' && b[p] == '<':
			depth--
			if depth == 0 {
				return b[p-1 : k], true
			}
			p -= 2
		default:
			p--
		}
	}
	return nil, false
}

var imageFilters = map[string]bool{
	"DCTDecode":      true,
	"JPXDecode":      true,
	"CCITTFaxDecode": true,
	"JBIG2Decode":    true,
}

// imageDict reports whether a stream dictionary marks its data as pixels:
// `/Subtype /Image` (an image XObject or its soft mask), or a `/Filter`, by
// name or in an array, of an image codec. Only the dictionary's own entries
// count, not those of a nested dictionary.
func imageDict(d []byte) bool {
	depth := 0
	for i := 0; i < len(d); {
		switch {
		case d[i] == '<' && i+1 < len(d) && d[i+1] == '<':
			depth++
			i += 2
		case d[i] == '>' && i+1 < len(d) && d[i+1] == '>':
			depth--
			i += 2
		case d[i] == '(':
			i = skipLiteral(d, i)
		case d[i] == '/' && depth == 1:
			name, j := readName(d, i)
			i = j
			switch name {
			case "Subtype":
				j = skipPDFSpace(d, j)
				if v, _ := readName(d, j); v == "Image" {
					return true
				}
			case "Filter":
				j = skipPDFSpace(d, j)
				if j < len(d) && d[j] == '/' {
					if v, _ := readName(d, j); imageFilters[v] {
						return true
					}
				} else if j < len(d) && d[j] == '[' {
					for j++; j < len(d) && d[j] != ']'; {
						if d[j] == '/' {
							var v string
							v, j = readName(d, j)
							if imageFilters[v] {
								return true
							}
							continue
						}
						j++
					}
				}
			}
		default:
			i++
		}
	}
	return false
}

func skipPDFSpace(d []byte, i int) int {
	for i < len(d) && isPDFSpace(d[i]) {
		i++
	}
	return i
}

// readName reads a name at d[i] == '/' and returns it without the slash.
func readName(d []byte, i int) (string, int) {
	if i >= len(d) || d[i] != '/' {
		return "", i
	}
	j := i + 1
	for j < len(d) && !isPDFDelim(d[j]) {
		j++
	}
	return string(d[i+1 : j]), j
}
