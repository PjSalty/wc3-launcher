//go:build !linux

package relay

import "syscall"

// reuseControl is a no-op off Linux. The relay daemon runs on Linux (the
// server); this stub only keeps the module cross-compilable for the launcher build.
func reuseControl(network, address string, c syscall.RawConn) error { return nil }
