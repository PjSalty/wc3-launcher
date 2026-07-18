package installer

import "strings"

// nuisanceDialogTitles are lowercased substrings of the dead-end dialogs the old
// Blizzard installer raises on modern Windows. They alarm non-technical players
// but are all safe to cancel:
//
//   - war3.htm readme fetch: the installer opens a Blizzard help page that no
//     longer exists (blizzard.com deleted it), so IE's download control pops a
//     "File Download - Security Warning" for the 404 object (http_404_webOC).
//   - Windows Help (.hlp / WinHlp32), a feature modern Windows dropped.
//
// Matching is deliberately narrow: none of these appear in the installer's own
// Setup / CD-key / install-location / progress windows, so closing a match can
// only ever dismiss a nuisance dialog. A non-match is a no-op.
var nuisanceDialogTitles = []string{
	"war3.htm",                          // dead Blizzard readme fetch -> 404
	"http_404_weboc",                    // the 404 object name in the save prompt
	"file download",                     // "File Download - Security Warning" title
	"how do you want to open this file", // Win10 open-with picker for .hlp
	"you'll need a new app",             // Win11 "open this .hlp file" prompt
	".hlp",                              // filename shown in the open-with title
	"winhlp32",                          // "Windows Help (WinHlp32)" not-supported box
	"windows help",                      // "Windows Help and Support" variants
	"32-bit help",                       // legacy "cannot display 32-bit Help"
}

// looksLikeNuisanceDialog reports whether a window title matches one of the
// old-installer dead-end dialogs that should be auto-cancelled. Case-insensitive
// substring match.
func looksLikeNuisanceDialog(title string) bool {
	t := strings.ToLower(title)
	for _, s := range nuisanceDialogTitles {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}
