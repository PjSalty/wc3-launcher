# Warcraft III PvPGN Launcher

Play Warcraft III: The Frozen Throne on your own PvPGN server: one download, no
setup, no port forwarding, no fiddling with gateways. Windows or Linux, a single
binary.

What makes it worth a look is the hosting. On a classic Warcraft III realm, the
player who creates a game has to be reachable from the internet, which normally
means forwarding a port and hoping your router cooperates. Here you just click
the game's own Create Game button and friends see it in Custom Games. A relay on
the server carries the game, so nobody opens a port, runs a bot, or patches the
PvPGN server it connects to. [ARCHITECTURE.md](ARCHITECTURE.md) walks through how
that works.

## For players

### Windows

1. Download `wc3-launcher.exe`.
2. Put it in an empty folder and double-click it.
3. If Warcraft III isn't already installed, it runs Blizzard's own official
   installer (follow the prompts, enter a CD key when asked). Then it adds the
   loader, sets up the connection, and launches. Later runs go straight in.

### Linux

1. Install Wine (`sudo apt install wine`, or use Lutris).
2. Download `wc3-launcher` and run it from a folder of its own.
3. Same flow, through a self-contained Wine prefix: Blizzard's installer runs
   under Wine, then the launcher adds the loader and launches the game.

That's it. Log in (or create an account in the game's Battle.net screen) and
you'll see the hosted games in the custom games list.

The game client is downloaded from Blizzard's official free endpoint, so this
launcher never redistributes Blizzard's files. It only ships the open-source
W3L loader and community maps. The server does not validate CD keys, so nothing
about the key matters once you are past Blizzard's installer.

You need to own Warcraft III: The Frozen Throne, and an account on the PvPGN
server to connect. The server address is set at build time (see below), so a
build points wherever it was built to point.

## What it actually does

- Downloads a preconfigured Warcraft III 1.28.5 folder plus the W3L loader and
  the current map pack (only when the bundle version changes).
- Points the Battle.net gateway at the PvPGN server by writing one
  `HKEY_CURRENT_USER` registry value. No administrator rights required. On
  Windows this is the real registry; on Linux it is the game's own Wine prefix.
- Launches the game through `w3l.exe`, the loader that lets a classic client
  talk to the PvPGN server (directly on Windows, via Wine on Linux).

The client connects on TCP 6112; that port is hardwired in Warcraft III.

Hosting is native: click the game's own Create Game button and friends see it in
Custom Games. Nobody opens a port or touches a router. The realm advertises a
game at the source address of the host's realm connection, and on the port the
client says it is hosting on. The launcher owns both: it tunnels the realm
session through the server (so the connection arrives from the server's address)
and writes the port the server handed it into the game's config before launch
(so the client declares that port itself). Joiners reach the server on that port
and get spliced back to the host through the same tunnel. No packets are
rewritten and the realm is stock.

Reforged (1.29+) cannot connect to a classic PvPGN server, so the bundle pins
1.28.5.

## Point it at your own server

The repo ships no server address and no credentials, so a plain `go build`
produces a launcher that points at the `wc3.example.com` placeholder and connects
to nothing. There are three ways to point it at a real server, checked in this
order (the first one set wins):

1. **First-run prompt.** Just run the binary. If nothing else is configured it
   asks for the server address (and an optional relay token and cert pin), saves
   your answers to a `wc3-launcher.json` next to the binary, and remembers them.
   Download, run, type it once. Delete that file to run setup again.
2. **Flags or environment.** `--server host`, `--token ...`, `--cert-pin ...`, or
   the `WC3_SERVER`, `WC3_RELAY_TOKEN`, `WC3_RELAY_CERT_PIN` environment variables.
3. **A config file.** `wc3-launcher.json` next to the binary, or in the per-user
   config dir (`%AppData%\wc3-launcher\config.json` on Windows,
   `~/.config/wc3-launcher/config.json` on Linux):

   ```json
   { "server": "realm.example.net", "token": "", "certPin": "" }
   ```

To bake the address in at build time instead (a zero-input binary for a known
group), set it with `-ldflags`, and it becomes the default when no runtime source
overrides it:

```bash
go build -trimpath -ldflags "\
  -s -w \
  -X main.serverHost=realm.example.net \
  -X main.relayToken=$RELAY_TOKEN \
  -X main.relayCertPin=$RELAY_CERT_PIN" -o dist/wc3-launcher .
```

Treat the token as a gate, not a secret: it keeps random scanners from opening a
tunnel, but anyone you hand a configured binary or token can read it back. Real
play access is the PvPGN account login. The published release builds are always
generic (no address or token compiled in), so a downloaded binary never carries
anyone's server details.

## For maintainers

```bash
go test ./...                                   # unit tests (incl. zip-slip guard)
go vet ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o dist/wc3-launcher.exe .
```

CI runs gofmt, vet, and tests on every push and pull request, and cross-compiles
both binaries on pushes to the default branch and on version tags. The gateway
name, bundle URL, and bundle version live in `config.go`; the server address,
tunnel token, and cert pin are injected at build time (see `version.go`) so none
of them live in source. Bump `bundleVersion` whenever the published bundle
changes so clients re-download.

The Battle.net gateway is set by writing registry values in `internal/client`:
`client_windows.go` on Windows, and `client_linux.go` on Linux, which drives the
same keys through the game's Wine prefix with `wine reg add`.
