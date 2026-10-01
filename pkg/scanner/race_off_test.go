//go:build !race

package scanner

// raceEnabled scales timing limits: the race detector slows the scan ~10x.
const raceEnabled = false
