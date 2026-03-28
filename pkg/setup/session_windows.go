//go:build windows

package setup

// ensureDBus is a no-op on Windows — DPAPI doesn't use DBus.
func ensureDBus() {}
