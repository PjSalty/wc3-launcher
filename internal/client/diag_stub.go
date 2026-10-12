//go:build !linux

package client

// HostDiagnostics reports host prerequisites for the doctor report. Off Linux
// there are none to check: Windows runs Warcraft III natively, so Wine, the
// Vulkan loader, and the GStreamer codecs are not in the picture at all.
//
// It still returns a line rather than nothing, so a Windows report visibly says
// "nothing to check here" instead of looking like a section that failed to run.
func HostDiagnostics(string) []Diag {
	return []Diag{{"host-prereqs", DiagOK, "none needed: Warcraft III runs natively on this platform"}}
}
