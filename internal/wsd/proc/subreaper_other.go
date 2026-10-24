//go:build !linux

package proc

// SetSubreaper is a no-op on non-Linux hosts. The explicit trusted local driver
// still enforces process groups and direct-child cleanup; production is Linux.
func SetSubreaper() error { return nil }
