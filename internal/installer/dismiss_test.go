package installer

import "testing"

// TestLooksLikeNuisanceDialog locks in the safety contract: every dead-end
// installer dialog must be recognized (so it gets auto-cancelled), and no
// installer or CD-key window may ever match (closing one would break the install).
func TestLooksLikeNuisanceDialog(t *testing.T) {
	dismiss := []string{
		// war3.htm 404 fetch (the real one, from the screenshot):
		"File Download - Security Warning",
		"0% of war3.htm from www.blizzard.com Completed",
		"http_404_webOC",
		// Windows Help variants:
		"How do you want to open this file?",
		"You'll need a new app to open this .hlp file",
		"Warcraft3_TFT.hlp",
		"Windows Help and Support",
		"Windows Help (WinHlp32.exe)",
		"Cannot display 32-bit Help",
	}
	for _, s := range dismiss {
		if !looksLikeNuisanceDialog(s) {
			t.Errorf("looksLikeNuisanceDialog(%q) = false, want true (help dialog must be dismissed)", s)
		}
	}

	// These are the installer's own windows and prompts. If any matched, the
	// watcher would close a step the player needs.
	keep := []string{
		"Warcraft III Setup",
		"Blizzard Entertainment",
		"Enter your CD Key",
		"Warcraft III: The Frozen Throne",
		"Choose Install Location",
		"Installing...",
		"",
	}
	for _, s := range keep {
		if looksLikeNuisanceDialog(s) {
			t.Errorf("looksLikeNuisanceDialog(%q) = true, want false (must not close installer/CD-key windows)", s)
		}
	}
}
