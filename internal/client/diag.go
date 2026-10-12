package client

// Diagnostics reporting for the launcher's `doctor` subcommand.
//
// The checks live next to the code that acts on them (prereqs_linux.go) and
// reuse its detection helpers verbatim - detectDistro, detectWineArch,
// vulkanLoaderPaths, gstreamerPluginDirs. That is the point: a doctor with its
// own parallel copy of the detection logic reports on a system the launcher
// does not actually see, which is worse than no doctor at all.

// DiagStatus is how one check turned out.
type DiagStatus int

const (
	DiagInfo DiagStatus = iota // context, nothing to act on
	DiagOK                     // checked and fine
	DiagWarn                   // works, but degraded or worth knowing
	DiagFail                   // this stops the game from running
)

func (s DiagStatus) String() string {
	switch s {
	case DiagOK:
		return "ok"
	case DiagWarn:
		return "warn"
	case DiagFail:
		return "FAIL"
	}
	return "info"
}

// Diag is one line of the report: a short stable name, a status, and detail
// written for whoever has to read the pasted output.
type Diag struct {
	Name   string
	Status DiagStatus
	Detail string
}
