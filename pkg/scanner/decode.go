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

	rd  bytes.Reader // reused for every inflate attempt
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

// childOverhead is the least any decoded buffer costs against the budget.
// A child costs max(len, childOverhead), so a flood of tiny outputs (a `%41`
// every four bytes, a short base64 run every 25) exhausts the budget instead
// of holding millions of small buffers in memory.
const childOverhead = 64

// Span kinds, in the pinned decoder order that breaks ties at one offset.
const (
	spanBase64 = iota
	spanHex
	spanURL
	spanInflate
	spanKinds
)

// cursors finds each decoder's spans lazily, one ahead, so nothing is listed
// up front: memory stays bounded by the budget whatever the input's shape.
type cursors struct {
	b       []byte
	inflate bool
	exempt  *exempt

	pos  [spanKinds]int  // where each decoder resumes its search
	off  [spanKinds]int  // offset of each decoder's pending span
	have [spanKinds]bool // whether a span is pending
	done [spanKinds]bool // whether the decoder has no more spans

	b64    b64Run
	hex    [2]int
	url    [2]int
	urlEnd int // end of the previous URL span
	infl   inflateAt
}

// fill makes sure decoder k has a pending span, unless it is done.
func (c *cursors) fill(k int) {
	if c.have[k] || c.done[k] {
		return
	}
	var ok bool
	switch k {
	case spanBase64:
		c.b64, c.pos[k], ok = nextBase64(c.b, c.pos[k])
		c.off[k] = c.b64.start
	case spanHex:
		c.hex, c.pos[k], ok = nextHex(c.b, c.pos[k])
		c.off[k] = c.hex[0]
	case spanURL:
		c.url, c.pos[k], ok = nextURL(c.b, c.pos[k], c.urlEnd)
		c.urlEnd = c.pos[k]
		c.off[k] = c.url[0]
	case spanInflate:
		if c.inflate {
			c.infl, c.pos[k], ok = nextInflate(c.b, c.pos[k], c.exempt)
			c.off[k] = c.infl.off
		}
	}
	c.have[k], c.done[k] = ok, !ok
}

// next returns the pending span with the lowest offset, ties going to the
// earlier decoder, and consumes it.
func (c *cursors) next() (int, bool) {
	kind := -1
	for k := 0; k < spanKinds; k++ {
		c.fill(k)
		if c.have[k] && (kind < 0 || c.off[k] < c.off[kind]) {
			kind = k
		}
	}
	if kind >= 0 {
		c.have[kind] = false
	}
	return kind, kind >= 0
}

// decode runs every decoder over n's buffer, spans in offset order and
// decoders in the pinned order at a shared offset, emitting each output. It
// reports whether the budget ran out. It returns early, without reporting
// exhaustion, when emit stops the scan.
func (s *scan) decode(n *node, inflate bool, emit emitFunc) (exhausted bool) {
	b := n.data
	c := &cursors{b: b, inflate: inflate}
	if inflate {
		c.exempt = newExempt(b)
	}

	// out emits a (possibly partial) output and says whether to go on.
	stop := false
	out := func(data []byte, step Step) bool {
		if len(data) > 0 {
			if len(data) < childOverhead {
				s.charge(int64(childOverhead - len(data)))
			}
			child := &node{data: data, field: n.field, depth: n.depth + 1, parent: n, step: step}
			if !emit(child) {
				stop = true
				return false
			}
		}
		return !s.exhausted()
	}

	for {
		kind, ok := c.next()
		if !ok {
			return false
		}
		switch kind {
		case spanBase64:
			r := c.b64
			for a := 0; a < 4; a++ {
				if !out(s.base64(r.data, a), Step{Decoder: DecoderBase64, Offset: r.start, Align: a}) {
					return !stop
				}
			}
		case spanHex:
			r := c.hex
			for a := 0; a < 2; a++ {
				if !out(s.hex(b[r[0]+a:r[1]]), Step{Decoder: DecoderHex, Offset: r[0], Align: a}) {
					return !stop
				}
			}
		case spanURL:
			r := c.url
			if !out(s.urlDecode(b[r[0]:r[1]]), Step{Decoder: DecoderURL, Offset: r[0]}) {
				return !stop
			}
		case spanInflate:
			at := c.infl
			dec := DecoderZlib
			if at.gzip {
				dec = DecoderGzip
			}
			if !out(s.inflate(b, at), Step{Decoder: dec, Offset: at.off}) {
				return !stop
			}
		}
	}
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

// nextBase64 finds the next base64 run at or after i: a maximal span of the
// alphabet with trailing `=` padding, at least 24 characters long. A line that
// is wholly alphabet characters, at least 40 of them after spaces and tabs are
// trimmed from both ends, joins the next line's run. It returns the run and
// where to resume.
func nextBase64(b []byte, i int) (b64Run, int, bool) {
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
		return b64Run{start: start, data: data}, i, true
	}
	return b64Run{}, len(b), false
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

// nextHex finds the next maximal run of at least 40 hex digits at or after
// i, and where to resume.
func nextHex(b []byte, i int) ([2]int, int, bool) {
	for i < len(b) {
		if !hexDigit[b[i]] {
			i++
			continue
		}
		s := i
		for i < len(b) && hexDigit[b[i]] {
			i++
		}
		if i-s >= 40 {
			return [2]int{s, i}, i, true
		}
	}
	return [2]int{}, len(b), false
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

// nextURL finds the next maximal non-whitespace span containing a `%XX`
// escape, searching from `from`; prevEnd is the end of the previous span. It
// returns the span and its end, which is where to resume.
func nextURL(b []byte, from, prevEnd int) ([2]int, int, bool) {
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
		return [2]int{s, e}, e, true
	}
	return [2]int{}, len(b), false
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

// nextInflate finds the next gzip (`1f 8b 08`) or valid zlib header offset
// at or after i, skipping offsets inside the exempt pixel-data spans ex yields. It
// returns the offset and where to resume.
func nextInflate(b []byte, i int, ex *exempt) (inflateAt, int, bool) {
	for ; i+1 < len(b); i++ {
		c := b[i]
		if c != 0x1f && c&0x0f != 8 {
			continue
		}
		if end, ok := ex.covers(i); ok {
			i = end - 1
			continue
		}
		if c == 0x1f {
			if b[i+1] == 0x8b && i+2 < len(b) && b[i+2] == 0x08 {
				return inflateAt{off: i, gzip: true}, i + 1, true
			}
			continue
		}
		if validZlib(c, b[i+1]) {
			return inflateAt{off: i}, i + 1, true
		}
	}
	return inflateAt{}, len(b), false
}

// inflate runs one gzip or zlib attempt at at.off and returns what it
// inflated, whole or partial. The attempt reads through a bytes.Reader, which
// compress/flate uses as its io.ByteReader without read-ahead, so the input
// charged is the inflater's real position. Input consumed and output produced
// are charged as they happen, and inflation stops at the budget.
func (s *scan) inflate(b []byte, at inflateAt) []byte {
	src := b[at.off:]
	s.rd.Reset(src)
	r := &s.rd
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
