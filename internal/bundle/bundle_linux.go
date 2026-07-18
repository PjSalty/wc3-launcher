//go:build linux

package bundle

import _ "embed"

// On Linux the overlay includes the DXVK d3d9.dll so WC3 renders reliably under
// Wine (Wine's native d3d9 path is unstable on NVIDIA/Wayland).
//
//go:embed assets/wc3-bundle-linux.zip
var asset []byte
