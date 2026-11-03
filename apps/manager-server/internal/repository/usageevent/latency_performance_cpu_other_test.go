//go:build !darwin && !linux

package usageevent

// CPU accounting is unavailable here; keep ordinary cross-platform tests valid.
func latencyPerfProcessCPU() float64 { return -1 }
