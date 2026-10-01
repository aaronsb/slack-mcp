package scanner

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
)

// scan carries one call's budget and the reusable inflaters.
type scan struct {
	budget   int64
	used     int64
	maxDepth int

	gz  gzip.Reader
	fl  io.ReadCloser // a flate decompressor, reused through flate.Resetter
	buf []byte
}

func newScan(opts Options) *scan {
	s := &scan{budget: opts.Budget, maxDepth: opts.MaxDepth}
	if s.budget <= 0 {
		s.budget = DefaultBudget
	}
	if s.maxDepth <= 0 {
		s.maxDepth = DefaultMaxDepth
	}
	return s
}

// charge takes up to n bytes from the budget and returns how many it took.
// Taking fewer than n means the budget is exhausted.
func (s *scan) charge(n int64) int64 {
	left := s.budget - s.used
	if n > left {
		n = left
	}
	if n < 0 {
		n = 0
	}
	s.used += n
	return n
}

func (s *scan) exhausted() bool { return s.used >= s.budget }

// emitFunc receives each decoded buffer as it is produced. Returning false
// stops the scan (a match).
type emitFunc func(*node) bool

// Span kinds, in the pinned decoder order that breaks ties at one offset.
const (
	spanBase64 = iota
	spanHex
	spanURL
	spanInflate
)

// decode runs every decoder over n's buffer, spans in offset order and
// decoders in the pinned order at a shared offset, emitting each output. It
// reports whether the budget ran out. It returns early, without reporting
// exhaustion, when emit stops the scan.
func (s *scan) decode(n *node, inflate bool, emit emitFunc) (exhausted bool) {
	b := n.data
	b64 := base64Runs(b)
	hex := hexRuns(b)
	url := urlSpans(b)
	var infl []inflateAt
	if inflate {
		infl = inflateOffsets(b, exemptSpans(b))
	}

	child := func(data []byte, step Step) *node {
		chain := make([]Step, len(n.chain)+1)
		copy(chain, n.chain)
		chain[len(n.chain)] = step
		return &node{data: data, field: n.field, depth: n.depth + 1, chain: chain}
	}
	// out emits a (possibly partial) output and says whether to go on.
	stop := false
	out := func(data []byte, step Step) bool {
		if len(data) > 0 && !emit(child(data, step)) {
			stop = true
			return false
		}
		return !s.exhausted()
	}

	var ib, ih, iu, ii int
	for !stop {
		kind, off := -1, 0
		pick := func(k, o int) {
			if kind < 0 || o < off {
				kind, off = k, o
			}
		}
		if ib < len(b64) {
			pick(spanBase64, b64[ib].start)
		}
		if ih < len(hex) {
			pick(spanHex, hex[ih][0])
		}
		if iu < len(url) {
			pick(spanURL, url[iu][0])
		}
		if ii < len(infl) {
			pick(spanInflate, infl[ii].off)
		}
		if kind < 0 {
			return false
		}
		switch kind {
		case spanBase64:
			r := b64[ib]
			ib++
			for a := 0; a < 4; a++ {
				if !out(s.base64(r.data, a), Step{Decoder: DecoderBase64, Offset: r.start, Align: a}) {
					return !stop
				}
			}
		case spanHex:
			r := hex[ih]
			ih++
			for a := 0; a < 2; a++ {
				if !out(s.hex(b[r[0]+a:r[1]]), Step{Decoder: DecoderHex, Offset: r[0], Align: a}) {
					return !stop
				}
			}
		case spanURL:
			r := url[iu]
			iu++
			if !out(s.urlDecode(b[r[0]:r[1]]), Step{Decoder: DecoderURL, Offset: r[0]}) {
				return !stop
			}
		case spanInflate:
			at := infl[ii]
			ii++
			dec := DecoderZlib
			if at.gzip {
				dec = DecoderGzip
			}
			if !out(s.inflate(b, at), Step{Decoder: dec, Offset: at.off}) {
				return !stop
			}
		}
	}
	return false
}

// ---- base64 ----

var b64Alphabet = byteSet("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/_-")

// b64Val maps the alphabet to 6-bit values; `-` and `_` decode as `+` and `/`.
var b64Val = func() (t [256]byte) {
	for i := range t {
		t[i] = 0xff
	}
	const std = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := 0; i < len(std); i++ {
		t[std[i]] = byte(i)
	}
	t['-'] = 62
	t['_'] = 63
	return t
}()

type b64Run struct {
	start int    // offset of the run's first character
	data  []byte // the run's alphabet characters, joined across lines, padding dropped
}

// base64Runs finds every base64 run: a maximal span of the alphabet with
// trailing `=` padding, at least 24 characters long. A line that is wholly
// alphabet characters, at least 40 of them after spaces and tabs are trimmed
// from both ends, joins the next line's run.
func base64Runs(b []byte) []b64Run {
	var runs []b64Run
	i := 0
	for i < len(b) {
		if !b64Alphabet[b[i]] {
			i++
			continue
		}
		start := i
		var pieces [][2]int // only when lines join
		first := [2]int{i, i}
		total := 0
		s := i
		for {
			e := s
			for e < len(b) && b64Alphabet[b[e]] {
				e++
			}
			if s == start {
				first[1] = e
			} else {
				if pieces == nil {
					pieces = append(pieces, first)
				}
				pieces = append(pieces, [2]int{s, e})
			}
			total += e - s
			// Does this run make up its whole line, at least 40 characters?
			if e-s < 40 || !wholeLine(b, s, e) {
				i = e
				break
			}
			nl := lineEnd(b, e)
			if nl >= len(b) {
				i = e
				break
			}
			next := nl + 1
			for next < len(b) && (b[next] == ' ' || b[next] == '\t') {
				next++
			}
			if next >= len(b) || !b64Alphabet[b[next]] {
				i = e
				break
			}
			s = next
		}
		pad := 0
		for i < len(b) && b[i] == '=' {
			i++
			pad++
		}
		if total+pad < 24 {
			continue
		}
		var data []byte
		if pieces == nil {
			data = b[first[0]:first[1]]
		} else {
			data = make([]byte, 0, total)
			for _, p := range pieces {
				data = append(data, b[p[0]:p[1]]...)
			}
		}
		runs = append(runs, b64Run{start: start, data: data})
	}
	return runs
}

// wholeLine reports whether [s,e) is all of its line once spaces and tabs
// are trimmed (a trailing `\r` counts as part of the line ending).
func wholeLine(b []byte, s, e int) bool {
	for j := s - 1; j >= 0 && b[j] != '\n'; j-- {
		if b[j] != ' ' && b[j] != '\t' {
			return false
		}
	}
	for j := e; j < len(b) && b[j] != '\n'; j++ {
		if b[j] != ' ' && b[j] != '\t' && !(b[j] == '\r' && (j+1 == len(b) || b[j+1] == '\n')) {
			return false
		}
	}
	return true
}

// lineEnd returns the index of the `\n` ending the line containing i, or
// len(b).
func lineEnd(b []byte, i int) int {
	if j := bytes.IndexByte(b[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(b)
}

// base64 decodes run at alignment a, dropping a leading characters,
// charging the output to the budget as produced. Padding is ignored: a
// trailing group of two or three characters decodes to the one or two whole
// bytes it holds, and only a final lone character, which holds no whole byte,
// is dropped.
func (s *scan) base64(run []byte, a int) []byte {
	if a >= len(run) {
		return nil
	}
	src := run[a:]
	want := len(src) / 4 * 3
	switch len(src) % 4 {
	case 2:
		want++
	case 3:
		want += 2
	}
	got := int(s.charge(int64(want)))
	out := make([]byte, 0, got+2)
	for i := 0; len(out) < got; i += 4 {
		var v uint32
		k := min(4, len(src)-i)
		for j := 0; j < 4; j++ {
			v <<= 6
			if j < k {
				v |= uint32(b64Val[src[i+j]])
			}
		}
		out = append(out, byte(v>>16), byte(v>>8), byte(v))
	}
	return out[:got]
}

// ---- hex ----

var hexDigit = byteSet("0123456789abcdefABCDEF")

// hexRuns finds every maximal run of at least 40 hex digits.
func hexRuns(b []byte) [][2]int {
	var runs [][2]int
	for i := 0; i < len(b); {
		if !hexDigit[b[i]] {
			i++
			continue
		}
		s := i
		for i < len(b) && hexDigit[b[i]] {
			i++
		}
		if i-s >= 40 {
			runs = append(runs, [2]int{s, i})
		}
	}
	return runs
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func (s *scan) hex(src []byte) []byte {
	got := s.charge(int64(len(src) / 2))
	out := make([]byte, got)
	for i := range out {
		out[i] = unhex(src[2*i])<<4 | unhex(src[2*i+1])
	}
	return out
}

// ---- URL encoding ----

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// urlSpans finds every maximal non-whitespace span containing a `%XX`
// escape.
func urlSpans(b []byte) [][2]int {
	var spans [][2]int
	from, prevEnd := 0, 0
	for from < len(b) {
		j := bytes.IndexByte(b[from:], '%')
		if j < 0 {
			break
		}
		p := from + j
		if p+2 >= len(b) || !hexDigit[b[p+1]] || !hexDigit[b[p+2]] {
			from = p + 1
			continue
		}
		// Spans are maximal and end at whitespace, so the walk back never
		// needs to pass the end of the previous span.
		s := p
		for s > prevEnd && !isSpace(b[s-1]) {
			s--
		}
		e := p
		for e < len(b) && !isSpace(b[e]) {
			e++
		}
		spans = append(spans, [2]int{s, e})
		from, prevEnd = e, e
	}
	return spans
}

// urlDecode percent-decodes src. `+` is left as is; a `%` not followed by
// two hex digits is kept.
func (s *scan) urlDecode(src []byte) []byte {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); i++ {
		if src[i] == '%' && i+2 < len(src) && hexDigit[src[i+1]] && hexDigit[src[i+2]] {
			out = append(out, unhex(src[i+1])<<4|unhex(src[i+2]))
			i += 2
			continue
		}
		out = append(out, src[i])
	}
	return out[:s.charge(int64(len(out)))]
}

// ---- gzip and zlib ----

type inflateAt struct {
	off  int
	gzip bool
}

// validZlib reports whether b[i:i+2] is a zlib header ADR-013 inflates:
// method 8, CINFO at most 7, FDICT clear, and CMF*256+FLG a multiple of 31.
func validZlib(cmf, flg byte) bool {
	return cmf&0x0f == 8 && cmf>>4 <= 7 && flg&0x20 == 0 && (uint16(cmf)<<8|uint16(flg))%31 == 0
}

// inflateOffsets lists every gzip (`1f 8b 08`) and valid zlib header offset
// in b, in offset order, skipping offsets inside exempt pixel-data spans.
func inflateOffsets(b []byte, exempt [][2]int) []inflateAt {
	var out []inflateAt
	ex := 0
	for i := 0; i+1 < len(b); i++ {
		c := b[i]
		if c != 0x1f && c&0x0f != 8 {
			continue
		}
		for ex < len(exempt) && exempt[ex][1] <= i {
			ex++
		}
		if ex < len(exempt) && exempt[ex][0] <= i {
			i = exempt[ex][1] - 1
			continue
		}
		if c == 0x1f {
			if b[i+1] == 0x8b && i+2 < len(b) && b[i+2] == 0x08 {
				out = append(out, inflateAt{off: i, gzip: true})
			}
			continue
		}
		if validZlib(c, b[i+1]) {
			out = append(out, inflateAt{off: i})
		}
	}
	return out
}

// inflate runs one gzip or zlib attempt at at.off and returns what it
// inflated, whole or partial. The attempt reads through a bytes.Reader, which
// compress/flate uses as its io.ByteReader without read-ahead, so the input
// charged is the inflater's real position. Input consumed and output produced
// are charged as they happen, and inflation stops at the budget.
func (s *scan) inflate(b []byte, at inflateAt) []byte {
	src := b[at.off:]
	r := bytes.NewReader(src)
	var charged int64
	chargeInput := func() bool {
		consumed := int64(len(src)) - int64(r.Len())
		d := consumed - charged
		charged = consumed
		return s.charge(d) == d
	}

	var rd io.Reader
	if at.gzip {
		err := s.gz.Reset(r)
		if !chargeInput() || err != nil {
			return nil
		}
		s.gz.Multistream(false)
		rd = &s.gz
	} else {
		_, _ = r.Seek(2, io.SeekStart)
		if s.fl == nil {
			s.fl = flate.NewReader(r)
		} else {
			_ = s.fl.(flate.Resetter).Reset(r, nil)
		}
		if !chargeInput() {
			return nil
		}
		rd = s.fl
	}

	if s.buf == nil {
		s.buf = make([]byte, 32<<10)
	}
	var out []byte
	for idle := 0; idle < 8; {
		n, err := rd.Read(s.buf)
		if !chargeInput() {
			return out
		}
		got := s.charge(int64(n))
		out = append(out, s.buf[:got]...)
		if got < int64(n) || err != nil {
			return out
		}
		if n == 0 {
			idle++
		}
	}
	return out
}

// byteSet builds a membership table for the bytes in chars.
func byteSet(chars string) *[256]bool {
	var t [256]bool
	for i := 0; i < len(chars); i++ {
		t[chars[i]] = true
	}
	return &t
}
