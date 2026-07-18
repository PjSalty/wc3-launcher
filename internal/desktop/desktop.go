// Package desktop installs a "Warcraft III (Online)" shortcut that points at
// the launcher, so non-technical players click one obvious icon and always go
// through the launcher's setup (gateway, loader, relay) instead of Blizzard's
// plain game. EnsureShortcut is platform-specific and best-effort: a failure
// never blocks launching the game.
package desktop

// shortcutName is the visible label on every platform's shortcut.
const shortcutName = "Warcraft III (Online)"
