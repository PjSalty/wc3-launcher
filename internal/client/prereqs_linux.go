//go:build linux

package client

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsurePrereqs makes sure Wine is available before we try to install or launch
// the game. Existing Wine is used exactly as-is and never modified. If Wine is
// missing, we detect the distro and offer to install it with the user's consent
// (via sudo). The dedicated Wine prefix means the user's own ~/.wine, packages,
// and settings are never touched.
func EnsurePrereqs() error {
	if _, err := exec.LookPath("wine"); err != nil {
		if err := installWine(); err != nil {
			return err
		}
	}
	// Wine is present (found or just installed). WC3 renders through DXVK, whose
	// 32-bit d3d9.dll needs a 32-bit Vulkan loader that a fresh install usually
	// lacks; without it the game crashes on startup (a flicker, then it closes).
	// Offer to install it. Best-effort: warn and continue, never block a launch
	// that would have worked, since the check is heuristic.
	ensureVulkan()
	// WC3 plays its intro cinematics (AVI) through Wine's GStreamer bridge. On a
	// fresh install the codec plugins (gst-libav + the avi demuxer) are usually
	// missing, winegstreamer fails to decode the stream, and the game crashes
	// right after the DXVK device comes up. Offer to install them. Best-effort.
	ensureGstreamer()
	return nil
}

// installWine detects the distro and installs Wine with the user's consent. The
// dedicated Wine prefix means the user's own ~/.wine is never touched.
func installWine() error {
	d := detectDistro()
	plan := d.wineInstall()

	fmt.Println()
	fmt.Println("Warcraft III on Linux runs through Wine, which is not installed yet.")

	if plan.manual {
		fmt.Printf("On %s I can't install it for you automatically. Install Wine with:\n\n", d.pretty)
		if plan.note != "" {
			fmt.Printf("    %s\n\n", plan.note)
		}
		if len(plan.commands) > 0 {
			fmt.Printf("    %s\n\n", plan.cmdline())
		}
		fmt.Println("Then run this launcher again.")
		return fmt.Errorf("wine is required and could not be auto-installed on %s", d.pretty)
	}

	fmt.Printf("I can install it for you on %s by running:\n\n    %s\n\n", d.pretty, plan.cmdline())
	if !promptYesNo("Install Wine now? (this asks for your sudo password)") {
		return fmt.Errorf("wine is required; install it and run the launcher again")
	}

	for _, c := range plan.commands {
		fmt.Printf("+ %s\n", strings.Join(c, " "))
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installing Wine (%s): %w", strings.Join(c, " "), err)
		}
	}
	if _, err := exec.LookPath("wine"); err != nil {
		return fmt.Errorf("wine still not found after install; open a new terminal and re-run, or install it manually")
	}
	fmt.Println("Wine is installed. Continuing.")
	return nil
}

// ensureVulkan checks for a 32-bit Vulkan loader and offers to install the
// Vulkan runtime when it looks missing. Best-effort: it warns and returns rather
// than failing, so it can never block a launch that would have worked.
func ensureVulkan() {
	if hasVulkan32() {
		return
	}
	d := detectDistro()
	plan := d.vulkanInstall()

	fmt.Println()
	fmt.Println("Warcraft III renders through DXVK (Vulkan), and a 32-bit Vulkan driver")
	fmt.Println("does not look installed. Without it the game crashes on startup (a")
	fmt.Println("flicker, then it closes).")

	if plan.manual || len(plan.commands) == 0 {
		if plan.note != "" {
			fmt.Printf("Install it with:\n\n    %s\n\n", plan.note)
		}
		fmt.Println("Then run the launcher again. Continuing for now in case your setup is fine.")
		return
	}

	fmt.Printf("I can install it on %s by running:\n\n    %s\n\n", d.pretty, plan.cmdline())
	if plan.note != "" {
		fmt.Printf("(%s)\n\n", plan.note)
	}
	if !promptYesNo("Install the 32-bit Vulkan driver now? (asks for your sudo password)") {
		fmt.Println("Skipping. If the game crashes on launch, install it and retry.")
		return
	}
	for _, c := range plan.commands {
		fmt.Printf("+ %s\n", strings.Join(c, " "))
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("(Vulkan install step failed: %v. Continuing; install it by hand if the game crashes.)\n", err)
			return
		}
	}
	fmt.Println("Vulkan driver installed. Continuing.")
}

// hasVulkan32 heuristically reports whether a 32-bit Vulkan loader is present, by
// looking for libvulkan.so.1 in the usual 32-bit library paths. Not exhaustive,
// but it catches the common fresh-install gap where only the 64-bit loader (or
// none) exists.
func hasVulkan32() bool {
	for _, p := range []string{
		"/usr/lib/i386-linux-gnu/libvulkan.so.1", // debian / ubuntu multiarch
		"/usr/lib32/libvulkan.so.1",              // arch and others
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// ensureGstreamer checks for the GStreamer codec plugins Wine needs to play WC3's
// intro videos (AVI) and offers to install them when they look missing. Best-
// effort: it warns and returns rather than failing, so it can never block a
// launch that would have worked.
func ensureGstreamer() {
	d := detectDistro()
	if d.hasGstreamerCodecs() {
		return
	}
	plan := d.gstreamerInstall()

	fmt.Println()
	fmt.Println("Warcraft III plays its intro videos through GStreamer, and the codec")
	fmt.Println("plugins for them (gst-libav and the AVI demuxer) do not look installed.")
	fmt.Println("Without them the game crashes right after it opens (the window appears,")
	fmt.Println("then closes with a video decode error).")

	if plan.manual || len(plan.commands) == 0 {
		if plan.note != "" {
			fmt.Printf("Install them with:\n\n    %s\n\n", plan.note)
		}
		fmt.Println("Then run the launcher again. Continuing for now in case your setup is fine.")
		return
	}

	fmt.Printf("I can install them on %s by running:\n\n    %s\n\n", d.pretty, plan.cmdline())
	if plan.note != "" {
		fmt.Printf("(%s)\n\n", plan.note)
	}
	if !promptYesNo("Install the GStreamer codecs now? (asks for your sudo password)") {
		fmt.Println("Skipping. If the game crashes on launch, install them and retry.")
		return
	}
	for _, c := range plan.commands {
		fmt.Printf("+ %s\n", strings.Join(c, " "))
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("(GStreamer codec install step failed: %v. Continuing; install by hand if the game crashes.)\n", err)
			return
		}
	}
	fmt.Println("GStreamer codecs installed. Continuing.")
}

// hasGstreamerCodecs reports whether the libav codec plugin is present in the
// GStreamer plugin dir that THIS distro's Wine actually loads for a 32-bit game.
// WC3 (1.28.x) is 32-bit: on the multilib-Wine distros (Debian/Fedora/SUSE) it
// loads the 32-bit host GStreamer, so the 32-bit plugin dir is what matters and a
// 64-bit-only libgstlibav.so does NOT count (a 32-bit process cannot load it).
// Arch is pure WoW64, where a 32-bit app uses the 64-bit host plugins, so there
// the 64-bit dir is the right one. Checking the wrong-bitness dir was why the
// v1.3.1 check falsely passed on multilib boxes and skipped the real fix.
func (d distro) hasGstreamerCodecs() bool {
	for _, dir := range d.gstreamerPluginDirs() {
		if _, err := os.Stat(filepath.Join(dir, "libgstlibav.so")); err == nil {
			return true
		}
	}
	return false
}

// gstreamerPluginDirs returns the plugin dir(s) that hold the codec Wine needs on
// this distro: the 32-bit multilib path on Debian/Fedora/SUSE, and the plain
// 64-bit path on Arch (WoW64). Unknown distros check the common 32-bit spots so we
// prompt rather than falsely skip.
func (d distro) gstreamerPluginDirs() []string {
	switch d.family() {
	case "debian":
		return []string{"/usr/lib/i386-linux-gnu/gstreamer-1.0"} // i386 multiarch
	case "fedora", "rhel", "suse":
		return []string{"/usr/lib/gstreamer-1.0"} // 32-bit multilib dir (/usr/lib64 is 64-bit)
	case "arch":
		return []string{"/usr/lib/gstreamer-1.0"} // WoW64: 64-bit host plugins
	}
	return []string{
		"/usr/lib/i386-linux-gnu/gstreamer-1.0",
		"/usr/lib32/gstreamer-1.0",
		"/usr/lib/gstreamer-1.0",
	}
}

// distro is the minimal identity we need from /etc/os-release.
type distro struct {
	id        string
	like      string
	pretty    string
	immutable bool
}

func detectDistro() distro {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return distro{id: "unknown", pretty: "your system"}
	}
	defer f.Close()
	d := parseOSRelease(f)
	// ostree-based systems (Fedora Silverblue/Kinoite, Bazzite, etc.) have a
	// read-only base image; package managers can't layer inline.
	if _, err := os.Stat("/run/ostree-booted"); err == nil {
		d.immutable = true
	}
	return d
}

// parseOSRelease reads os-release key/values and derives the distro identity. It
// takes a reader so it can be unit-tested without touching the host.
func parseOSRelease(r io.Reader) distro {
	vals := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		vals[k] = strings.Trim(v, `"'`)
	}
	d := distro{id: "unknown", pretty: "your system"}
	if v := vals["ID"]; v != "" {
		d.id = v
	}
	d.like = vals["ID_LIKE"]
	if v := vals["PRETTY_NAME"]; v != "" {
		d.pretty = v
	}
	variant := strings.ToLower(vals["VARIANT_ID"] + " " + vals["ID"])
	if containsAny(variant, "silverblue", "kinoite", "bazzite", "sericea", "onyx") {
		d.immutable = true
	}
	return d
}

// family maps the distro to its package ecosystem.
func (d distro) family() string {
	all := strings.ToLower(d.id + " " + d.like)
	switch {
	case containsAny(all, "arch", "manjaro", "endeavouros", "cachyos"):
		return "arch"
	case containsAny(all, "debian", "ubuntu", "linuxmint", "pop", "elementary"):
		return "debian"
	case containsAny(all, "rhel", "centos", "rocky", "almalinux", "ol"):
		return "rhel"
	case containsAny(all, "fedora"):
		return "fedora"
	case containsAny(all, "opensuse", "suse", "sles"):
		return "suse"
	}
	return "unknown"
}

// installPlan is how we would install Wine on this distro. manual=true means we
// cannot run it inline (immutable base or unknown distro) and only show it.
type installPlan struct {
	commands [][]string
	manual   bool
	note     string
}

func (p installPlan) cmdline() string {
	parts := make([]string, 0, len(p.commands))
	for _, c := range p.commands {
		parts = append(parts, strings.Join(c, " "))
	}
	return strings.Join(parts, " && ")
}

func (d distro) wineInstall() installPlan {
	if d.immutable {
		// Least invasive on an atomic base: a Flatpak Wine, which does not modify
		// the read-only OS image. Shown, not auto-run.
		return installPlan{
			manual:   true,
			note:     "flatpak install -y flathub org.winehq.Wine",
			commands: [][]string{{"flatpak", "install", "-y", "flathub", "org.winehq.Wine"}},
		}
	}
	switch d.family() {
	case "arch":
		// Wine needs the multilib repo enabled; if it is not, this errors clearly
		// and the user enables it, rather than us editing pacman.conf for them.
		return installPlan{commands: [][]string{
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "wine", "wine-mono", "wine-gecko"},
		}}
	case "debian":
		return installPlan{commands: [][]string{
			{"sudo", "dpkg", "--add-architecture", "i386"},
			{"sudo", "apt-get", "update"},
			{"sudo", "apt-get", "install", "-y", "wine", "wine64", "wine32"},
		}}
	case "fedora":
		return installPlan{commands: [][]string{
			{"sudo", "dnf", "install", "-y", "wine"},
		}}
	case "rhel":
		return installPlan{commands: [][]string{
			{"sudo", "dnf", "install", "-y", "epel-release"},
			{"sudo", "dnf", "install", "-y", "wine"},
		}}
	case "suse":
		return installPlan{commands: [][]string{
			{"sudo", "zypper", "--non-interactive", "install", "wine"},
		}}
	}
	return installPlan{manual: true, note: "install the 'wine' package with your distro's package manager"}
}

// vulkanInstall is how we would install a 32-bit Vulkan loader + driver on this
// distro. WC3 1.28 is a 32-bit app, so the 64-bit loader alone is not enough.
func (d distro) vulkanInstall() installPlan {
	if d.immutable {
		return installPlan{manual: true, note: "install a 32-bit Vulkan driver via your image tooling (rpm-ostree/Flatpak runtime)"}
	}
	switch d.family() {
	case "arch":
		return installPlan{
			commands: [][]string{{"sudo", "pacman", "-S", "--needed", "--noconfirm", "vulkan-icd-loader", "lib32-vulkan-icd-loader"}},
			note:     "also install your GPU's 32-bit driver: lib32-mesa (AMD/Intel) or lib32-nvidia-utils (NVIDIA)",
		}
	case "debian":
		return installPlan{
			commands: [][]string{
				{"sudo", "dpkg", "--add-architecture", "i386"},
				{"sudo", "apt-get", "update"},
				{"sudo", "apt-get", "install", "-y", "libvulkan1", "libvulkan1:i386", "mesa-vulkan-drivers", "mesa-vulkan-drivers:i386"},
			},
			note: "for NVIDIA also install the 32-bit NVIDIA Vulkan driver (libnvidia-gl-<ver>:i386)",
		}
	case "fedora", "rhel":
		return installPlan{
			commands: [][]string{{"sudo", "dnf", "install", "-y", "vulkan-loader.i686", "mesa-vulkan-drivers.i686"}},
			note:     "for NVIDIA install the 32-bit NVIDIA Vulkan driver from RPM Fusion",
		}
	case "suse":
		return installPlan{commands: [][]string{{"sudo", "zypper", "--non-interactive", "install", "libvulkan1-32bit", "Mesa-libGL1-32bit"}}}
	}
	return installPlan{manual: true, note: "install the 32-bit Vulkan loader (libvulkan1:i386 or lib32-vulkan-icd-loader) and your GPU's 32-bit Vulkan driver"}
}

// gstreamerInstall is how we would install the GStreamer codec plugins WC3's
// intro cinematics need through Wine: the AVI demuxer (plugins-good) plus the
// libav/ffmpeg video decoders (gst-libav).
func (d distro) gstreamerInstall() installPlan {
	if d.immutable {
		return installPlan{manual: true, note: "install the GStreamer codec plugins (gst-libav + plugins-good/bad/ugly, including the 32-bit variants for Wine) via your image tooling or a Flatpak runtime"}
	}
	switch d.family() {
	case "arch":
		// Arch is pure WoW64: a 32-bit app uses the 64-bit host GStreamer, so the
		// 64-bit plugins are the ones Wine loads. No lib32 packages needed.
		return installPlan{commands: [][]string{
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "gst-libav", "gst-plugins-good", "gst-plugins-bad", "gst-plugins-ugly"},
		}}
	case "debian":
		// Multilib Wine: WC3 is 32-bit, so winegstreamer loads the i386 plugins; a
		// 32-bit process cannot dlopen a 64-bit .so. Enable i386 and install both
		// bitnesses (the :i386 set is the one that actually fixes the crash).
		return installPlan{commands: [][]string{
			{"sudo", "dpkg", "--add-architecture", "i386"},
			{"sudo", "apt-get", "update"},
			{"sudo", "apt-get", "install", "-y",
				"gstreamer1.0-libav", "gstreamer1.0-plugins-good", "gstreamer1.0-plugins-bad", "gstreamer1.0-plugins-ugly",
				"gstreamer1.0-libav:i386", "gstreamer1.0-plugins-good:i386", "gstreamer1.0-plugins-bad:i386", "gstreamer1.0-plugins-ugly:i386"},
		}}
	case "fedora", "rhel":
		// The 32-bit (.i686) plugins are the ones a 32-bit winegstreamer loads.
		// gstreamer1-plugin-libav (ffmpeg-free, main repo) is the current name; the
		// old RPM Fusion gstreamer1-libav is retired.
		return installPlan{
			commands: [][]string{{"sudo", "dnf", "install", "-y",
				"gstreamer1-plugin-libav", "gstreamer1-plugins-good", "gstreamer1-plugins-bad-free", "gstreamer1-plugins-ugly-free",
				"gstreamer1-plugin-libav.i686", "gstreamer1-plugins-good.i686", "gstreamer1-plugins-bad-free.i686", "gstreamer1-plugins-ugly-free.i686"}},
			note: "if an intro codec is still missing, enable RPM Fusion for the extra freeworld plugins",
		}
	case "suse":
		// openSUSE ships 32-bit multilib as -32bit packages.
		return installPlan{commands: [][]string{{"sudo", "zypper", "--non-interactive", "install",
			"gstreamer-plugins-libav", "gstreamer-plugins-good", "gstreamer-plugins-bad", "gstreamer-plugins-ugly",
			"gstreamer-plugins-libav-32bit", "gstreamer-plugins-good-32bit", "gstreamer-plugins-bad-32bit", "gstreamer-plugins-ugly-32bit"}}}
	}
	return installPlan{manual: true, note: "install the GStreamer codec plugins gst-libav + gst-plugins-good/bad/ugly, including the 32-bit variants Wine needs for a 32-bit game"}
}

func promptYesNo(q string) bool {
	fmt.Printf("%s [Y/n]: ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "" || line == "y" || line == "yes"
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}
