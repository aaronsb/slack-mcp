//go:build windows

package setup

// ensureDisplay is a no-op on Windows — Chrome uses native windowing.
func ensureDisplay() {}
