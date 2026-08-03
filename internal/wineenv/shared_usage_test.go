package wineenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryWineInvocationUsesSharedEnv is the guard for the bug that shipped in
// v1.3.9: the XDG_RUNTIME_DIR repair lived in internal/client, while
// internal/installer built its own environment and therefore kept failing. Any
// file that runs Wine must take its environment from this package, so a repair
// here reaches every path.
func TestEveryWineInvocationUsesSharedEnv(t *testing.T) {
	root := filepath.Join("..", "..")
	runsWine := regexp.MustCompile(`exec\.Command\(\s*wine`)
	var offenders []string

	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s := string(b)
		if !runsWine.MatchString(s) {
			return nil
		}
		if !strings.Contains(s, "wineenv.RuntimeDirEnv") {
			offenders = append(offenders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these files run Wine without the shared environment repair: %v", offenders)
	}
}
