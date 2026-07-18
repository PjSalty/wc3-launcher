//go:build windows

package installer

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The old Blizzard installer, on Windows versions that dropped Windows Help,
// raises a "you need a help file / how do you want to open this .hlp" dialog
// that non-technical players find alarming. While the installer runs we poll
// the top-level windows and close any that match looksLikeNuisanceDialog. The match
// is help-specific, so this never touches the installer's own Setup or CD-key
// windows; if nothing matches, nothing happens.

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows     = user32.NewProc("EnumWindows")
	procGetWindowTextW  = user32.NewProc("GetWindowTextW")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procPostMessageW    = user32.NewProc("PostMessageW")
)

const wmClose = 0x0010

// enumHelpDialogsProc is created ONCE. syscall.NewCallback callbacks cannot be
// freed and the process has a hard limit on how many exist, so it must never be
// created inside the poll loop. It closes each visible window whose title looks
// like the help nuisance dialog.
var enumHelpDialogsProc = syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return 1 // keep enumerating
	}
	buf := make([]uint16, 256)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 && looksLikeNuisanceDialog(windows.UTF16ToString(buf[:n])) {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	return 1 // keep enumerating
})

// dismissHelpDialogs closes any currently-open Windows Help nuisance dialogs.
func dismissHelpDialogs() { procEnumWindows.Call(enumHelpDialogsProc, 0) }

// watchAndDismissHelp closes the help dialog whenever it appears, until stop is
// closed. The installer can raise it more than once, so we watch for the whole
// run rather than dismissing a single time.
func watchAndDismissHelp(stop <-chan struct{}) {
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			dismissHelpDialogs()
		}
	}
}
