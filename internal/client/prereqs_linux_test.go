//go:build linux

package client

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseOSReleaseAndFamily(t *testing.T) {
	cases := []struct {
		name       string
		osRelease  string
		wantFamily string
		immutable  bool
	}{
		{
			name:       "arch",
			osRelease:  "ID=arch\nPRETTY_NAME=\"Arch Linux\"\n",
			wantFamily: "arch",
		},
		{
			name:       "ubuntu via ID_LIKE debian",
			osRelease:  "ID=ubuntu\nID_LIKE=debian\nPRETTY_NAME=\"Ubuntu 24.04\"\n",
			wantFamily: "debian",
		},
		{
			name:       "fedora",
			osRelease:  "ID=fedora\nPRETTY_NAME=\"Fedora Linux 40\"\n",
			wantFamily: "fedora",
		},
		{
			name:       "rocky via ID_LIKE rhel",
			osRelease:  `ID="rocky"` + "\n" + `ID_LIKE="rhel centos fedora"` + "\n",
			wantFamily: "rhel",
		},
		{
			name:       "bazzite is immutable",
			osRelease:  "ID=bazzite\nID_LIKE=fedora\nVARIANT_ID=bazzite\n",
			wantFamily: "fedora",
			immutable:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := parseOSRelease(strings.NewReader(tc.osRelease))
			if got := d.family(); got != tc.wantFamily {
				t.Errorf("family = %q, want %q", got, tc.wantFamily)
			}
			if d.immutable != tc.immutable {
				t.Errorf("immutable = %v, want %v", d.immutable, tc.immutable)
			}
		})
	}
}

func TestWineInstallPlan(t *testing.T) {
	// A normal distro yields a runnable sudo plan.
	arch := distro{id: "arch"}
	p := arch.wineInstall()
	if p.manual {
		t.Fatal("arch plan should be auto-runnable, not manual")
	}
	if !strings.Contains(p.cmdline(), "pacman") {
		t.Errorf("arch plan should use pacman, got %q", p.cmdline())
	}

	// An immutable distro must NOT be auto-run (no editing the base image).
	bazzite := distro{id: "bazzite", like: "fedora", immutable: true}
	pb := bazzite.wineInstall()
	if !pb.manual {
		t.Error("immutable distro plan must be manual (not auto-run)")
	}
	if !strings.Contains(pb.note+pb.cmdline(), "flatpak") {
		t.Errorf("immutable plan should suggest flatpak, got note=%q cmd=%q", pb.note, pb.cmdline())
	}

	// Debian enables i386 before installing (32-bit Wine).
	deb := distro{id: "debian"}
	pd := deb.wineInstall()
	if !strings.Contains(pd.cmdline(), "i386") {
		t.Errorf("debian plan should add i386 arch, got %q", pd.cmdline())
	}
}

// TestVulkanInstallPlans locks in the per-distro 32-bit Vulkan loader install
// commands. Installing that loader is the fix for the fresh-install launch crash
// (DXVK's 32-bit d3d9 needs it), so each distro must name the right package.
func TestVulkanInstallPlans(t *testing.T) {
	want := map[string]string{
		"arch":   "lib32-vulkan-icd-loader",
		"debian": "libvulkan1:i386",
		"fedora": "vulkan-loader.i686",
		"suse":   "libvulkan1-32bit",
	}
	for id, pkg := range want {
		plan := distro{id: id}.vulkanInstall()
		var cmds []string
		for _, c := range plan.commands {
			cmds = append(cmds, strings.Join(c, " "))
		}
		if joined := strings.Join(cmds, " | "); !strings.Contains(joined, pkg) {
			t.Errorf("distro %q vulkan plan missing %q: %q (note %q)", id, pkg, joined, plan.note)
		}
	}
}

// TestGstreamerInstallPlans locks in the per-distro GStreamer codec install
// commands. gst-libav (plus the good/bad/ugly demuxers) is the fix for the
// fresh-install crash right after DXVK init: Wine plays WC3's intro AVI through
// GStreamer and dies when the codec is missing. Each distro must name it.
func TestGstreamerInstallPlans(t *testing.T) {
	// Each distro's plan must name the libav codec package (the verified current
	// name) AND, on multilib-Wine distros, the 32-bit variant marker: WC3 is a
	// 32-bit game, so its winegstreamer needs 32-bit plugins. Installing only the
	// 64-bit package was the v1.3.1 bug that left the crash unfixed on those
	// distros. Arch is pure WoW64, so it correctly ships 64-bit only.
	want := map[string]struct{ libav, bitness string }{
		"arch":   {"gst-libav", ""}, // WoW64: 64-bit is correct, no 32-bit marker
		"debian": {"gstreamer1.0-libav", ":i386"},
		"fedora": {"gstreamer1-plugin-libav", ".i686"},
		"rhel":   {"gstreamer1-plugin-libav", ".i686"},
		"suse":   {"gstreamer-plugins-libav", "-32bit"},
	}
	for id, w := range want {
		plan := distro{id: id}.gstreamerInstall()
		var cmds []string
		for _, c := range plan.commands {
			cmds = append(cmds, strings.Join(c, " "))
		}
		joined := strings.Join(cmds, " | ")
		if !strings.Contains(joined, w.libav) {
			t.Errorf("distro %q gstreamer plan missing libav pkg %q: %q (note %q)", id, w.libav, joined, plan.note)
		}
		if w.bitness != "" && !strings.Contains(joined, w.libav+w.bitness) {
			t.Errorf("distro %q gstreamer plan missing 32-bit libav variant %q (32-bit WC3 needs it): %q", id, w.libav+w.bitness, joined)
		}
	}

	// The retired RPM Fusion name must NOT come back on fedora.
	if joined := planCmds(distro{id: "fedora"}.gstreamerInstall()); strings.Contains(joined, "gstreamer1-libav ") || strings.Contains(joined, "gstreamer1-libav.") {
		t.Errorf("fedora plan uses the retired gstreamer1-libav name: %q", joined)
	}

	// An immutable distro must NOT be auto-run (no editing the base image).
	pb := distro{id: "bazzite", like: "fedora", immutable: true}.gstreamerInstall()
	if !pb.manual {
		t.Error("immutable distro gstreamer plan must be manual (not auto-run)")
	}
}

func planCmds(p installPlan) string {
	var cmds []string
	for _, c := range p.commands {
		cmds = append(cmds, strings.Join(c, " "))
	}
	return strings.Join(cmds, " | ")
}

// TestEnsurePrereqsNoOpWhenPresent runs the real preflight on the host. On a box
// that already has Wine, a 32-bit Vulkan loader, and the GStreamer codecs it must
// be a clean no-op: no prompt, no error. It SKIPS (never installs anything) when
// any is absent, so it is safe in CI and only actually exercises a host that has
// all three.
func TestEnsurePrereqsNoOpWhenPresent(t *testing.T) {
	if _, err := exec.LookPath("wine"); err != nil {
		t.Skip("wine not present; nothing to verify without installing")
	}
	if !hasVulkan32() {
		t.Skip("32-bit vulkan loader not present; would prompt to install")
	}
	if !detectDistro().hasGstreamerCodecs() {
		t.Skip("gstreamer codecs not present; would prompt to install")
	}
	if err := EnsurePrereqs(); err != nil {
		t.Fatalf("EnsurePrereqs errored on a host with wine + vulkan + gstreamer present: %v", err)
	}
}
