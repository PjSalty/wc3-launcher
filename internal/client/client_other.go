//go:build !windows && !linux

package client

import (
	"fmt"
	"os/exec"
)

// Configure is unsupported outside Windows and Linux.
func Configure(dir, host, name, timezone string) error {
	return fmt.Errorf("this launcher supports Windows and Linux only")
}

// Launch is unsupported outside Windows and Linux.
func Launch(dir, gameRoot, loaderExe string, classic bool) (*exec.Cmd, error) {
	return nil, fmt.Errorf("this launcher supports Windows and Linux only")
}

// gameRunning is unsupported off Windows/Linux; report false so WaitForGameExit
// stays resident and never signals a false exit.
func gameRunning() bool { return false }

// ExistingGateway is unsupported here; nothing to migrate.
func ExistingGateway(dir string) (host, name string, ok bool) { return "", "", false }

// SetGamePort is unsupported outside Windows and Linux.
func SetGamePort(dir string, port int) error {
	return fmt.Errorf("this launcher supports Windows and Linux only")
}
