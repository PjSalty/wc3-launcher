//go:build windows

package bundle

import _ "embed"

// On Windows the overlay carries only the W3L loader and d3d8to9 (which forwards
// Direct3D 8 to the system's native Direct3D 9). It deliberately omits the 7.8 MB
// DXVK d3d9.dll: that is only needed for Wine, and shipping it would bloat the
// Windows download and force an unnecessary Vulkan dependency on native Windows.
//
//go:embed assets/wc3-bundle-windows.zip
var asset []byte
