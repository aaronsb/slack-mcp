package scanner

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// peakInputs are adversarial shapes for time and memory. Each is sized so the
// scan's own memory, not the input, is what grows if decoding is unbounded.
var peakInputs = map[string]func() []Field{
	// A URL span every four bytes.
	"url": func() []Field { return file(bytes.Repeat([]byte("%41 "), 50<<20/4)) },
	// A valid zlib header every two bytes.
	"zlib-headers": func() []Field { return file(bytes.Repeat([]byte("x\x01"), 50<<20/2)) },
	// A minimum-length base64 run every 25 bytes.
	"base64-short": func() []Field { return file(bytes.Repeat([]byte("QUFBQUFBQUFBQUFBQUFBQUFB "), 50<<20/25)) },
	// Stream keywords with no endstream.
	"pdf-stream": func() []Field {
		return file(append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("stream\n"), 1<<20/7)...))
	},
	// Stream keywords, each closed at once.
	"pdf-endstream": func() []Field {
		return file(append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte(">>stream\n endstream\n"), 4<<20/20)...))
	},
	// Ten thousand rich_text elements on one line.
	"env-elements": func() []Field {
		data := []byte(strings.Repeat("a", 10000))
		starts := make([]int, 10000)
		for i := range starts {
			starts[i] = i
		}
		return []Field{{Kind: FieldText, Data: data, ElementStarts: starts}}
	},
	"jpeg-50mib": func() []Field { return file(jpegLike(50<<20, 8)) },
}

// TestPeakMemory runs one adversarial input, named by SCANNER_PEAK, and logs
// the time and the process's peak RSS (VmHWM). It is a measuring tool, run
// one input per process:
//
//	SCANNER_PEAK=url go test -run TestPeakMemory -v ./pkg/scanner
func TestPeakMemory(t *testing.T) {
	name := os.Getenv("SCANNER_PEAK")
	if name == "" || runtime.GOOS != "linux" {
		t.Skip("set SCANNER_PEAK to one of the peakInputs (Linux only)")
	}
	fields := peakInputs[name]()
	var in int
	for _, f := range fields {
		in += len(f.Data)
	}
	start := time.Now()
	r := Scan(fields, Options{})
	d := time.Since(start)
	status, _ := os.ReadFile("/proc/self/status")
	hwm := ""
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			hwm = strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
		}
	}
	t.Logf("%s: input %d MiB, %v, peak RSS %s, blocked=%v unscannable=%v used=%d",
		name, in>>20, d, hwm, r.Blocked(), r.Unscannable, r.BudgetUsedForLog())
}
