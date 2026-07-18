//go:build !linux

package client

// EnsurePrereqs is a no-op off Linux. Windows runs the game natively and needs
// no Wine or extra packages.
func EnsurePrereqs() error { return nil }
