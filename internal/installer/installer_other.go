//go:build !windows && !linux

package installer

import "fmt"

// InstallPath is unsupported outside Windows and Linux.
func InstallPath(dir string) (string, bool) { return "", false }

// Run is unsupported outside Windows and Linux.
func Run(dir, installerPath string) error {
	return fmt.Errorf("this launcher supports Windows and Linux only")
}
