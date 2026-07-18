//go:build linux

package client

import "testing"

// TestGameRunningNoFalsePositive is the safety guard for auto-close: no Warcraft
// III is running under test, so gameRunning() MUST report false. A false positive
// here is exactly the failure that closed the launcher (and killed the relay)
// mid-game, so this pins it down.
func TestGameRunningNoFalsePositive(t *testing.T) {
	if gameRunning() {
		t.Fatal("gameRunning() reported a game running with none present; a false positive closes the relay early")
	}
}
