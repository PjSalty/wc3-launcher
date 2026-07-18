//go:build !windows && !linux

package desktop

// EnsureShortcut is a no-op on unsupported platforms.
func EnsureShortcut(launcherPath, gameRoot string) error { return nil }

// RepointGameShortcuts is a no-op on unsupported platforms.
func RepointGameShortcuts(launcherPath, gameRoot string) (int, error) { return 0, nil }
