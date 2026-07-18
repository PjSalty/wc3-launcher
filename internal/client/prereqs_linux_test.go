//go:build linux

package client

import (
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
