//go:build unix

package exchange

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLooseModeIsRefusedNotRepaired(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	p := d.Path()
	d.Close()
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Open()
	if err == nil || !strings.Contains(err.Error(), "chmod 700 "+p) {
		t.Fatalf("0755 exchange dir: %v", err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode was repaired to %o", fi.Mode().Perm())
	}
}

func TestCreatedModes(t *testing.T) {
	isolate(t)
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	d := mustOpen(t)
	if fi, _ := os.Stat(d.Path()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("exchange dir mode %o, want 700", fi.Mode().Perm())
	}
	res, err := d.Create("x.bin")
	if err != nil {
		t.Fatal(err)
	}
	res.File.Close()
	if fi, _ := os.Stat(filepath.Join(d.Path(), "x.bin")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o, want 600", fi.Mode().Perm())
	}
}

func TestFIFODoesNotHangTheRead(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	if err := syscall.Mkfifo(filepath.Join(d.Path(), "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.Open("pipe", 100)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO read: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
}

func TestDirectorySwappedBeforeOpenRootIsRefused(t *testing.T) {
	isolate(t)
	d := mustOpen(t)
	p := d.Path()
	d.Close()

	afterLstat = func(path string) {
		if err := os.Rename(path, path+".old"); err != nil {
			t.Errorf("rename: %v", err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Errorf("mkdir: %v", err)
		}
	}
	t.Cleanup(func() { afterLstat = nil })

	_, err := Open()
	if err == nil || !strings.Contains(err.Error(), "changed between the check and the open") {
		t.Fatalf("swapped directory at %s: %v", p, err)
	}
}
