package scanner

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// jpegLike is a JPEG-shaped high-entropy buffer: the SOI/APP0 magic, then
// bytes the scanner does not decode.
func jpegLike(n int, seed int64) []byte {
	b := randomBytes(n, seed)
	copy(b, []byte{0xff, 0xd8, 0xff, 0xe0})
	return b
}

// A large high-entropy upload costs scan time in proportion to its size and
// little budget, at any resolution.
func TestPerformanceLargeJPEGLike(t *testing.T) {
	buf := jpegLike(50<<20, 5)
	start := time.Now()
	r := Scan(file(buf), Options{})
	d := time.Since(start)
	mustClean(t, r)
	t.Logf("50 MiB JPEG-like: %v, %.1f MiB/s, budget used %d bytes (%.2f%%)",
		d, 50/d.Seconds(), r.BudgetUsedForLog(), 100*float64(r.BudgetUsedForLog())/float64(len(buf)))
	if d > 2*time.Minute {
		t.Fatalf("took %v", d)
	}
}

func benchScan(b *testing.B, fields []Field) {
	var n int64
	for _, f := range fields {
		n += int64(len(f.Data))
	}
	b.SetBytes(n)
	b.ReportAllocs()
	var used int64
	for b.Loop() {
		used = Scan(fields, Options{}).BudgetUsedForLog()
	}
	b.ReportMetric(float64(used), "budget-bytes")
}

func BenchmarkScanRandom8MiB(b *testing.B) { benchScan(b, file(jpegLike(8<<20, 6))) }

func BenchmarkScanProse64KiB(b *testing.B) {
	prose := strings.Repeat("The deploy finished at 14:02; see https://example.com/runs/1234 for logs. ", 900)
	benchScan(b, text(prose[:64<<10]))
}

func BenchmarkScanBase64Heavy1MiB(b *testing.B) {
	benchScan(b, file([]byte(b64(randomBytes(768<<10, 7)))))
}

func BenchmarkScanEnvFile(b *testing.B) {
	var env bytes.Buffer
	for i := 0; i < 200; i++ {
		env.WriteString("SERVICE_NAME_" + strings.Repeat("A", i%7) + "=prod-db-credentials\nMAX_TOKENS=4096\n")
	}
	benchScan(b, text(env.String()))
}
