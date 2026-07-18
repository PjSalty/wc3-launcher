# Architecture

How a from-scratch launcher makes native Warcraft III **Create Game** hosting work
through a private PvPGN server, with no port forwarding and no router config on
anyone's machine.

The short version: a friend clicks the game's own Create Game button, and other
players see the game in Custom Games and join it. No bot, no PvPGN patch, no port
opened on the host's router. A small relay on the server carries the game.

The rest of this doc is how that actually works, grounded in the code.

## 1. The problem

Warcraft III's custom-game hosting predates NAT being universal. When you create a
game, the realm (PvPGN) advertises it to everyone else at a single address:

    <source IP of your realm TCP connection> : <the port your client declared>

Both halves come from you, and on a modern home connection both are wrong for
anyone outside your LAN:

- **The IP** is whatever the realm sees as the peer of your Battle.net (BNCS)
  connection. Behind NAT that's your router's WAN address, and nothing is
  forwarded back in, so a joiner's TCP connect to it just times out. PvPGN reads
  this straight off the BNCS socket's peer (the reuse note in
  `internal/relay/dial_linux.go` points at PvPGN's `connection.cpp`: game
  address and port default to the bnet TCP peer).
- **The port** is the value the client sends in `SID_NETGAMEPORT`, which it reads
  from its own registry (`netgameport`, default 6112). Even if the IP were
  reachable, that port has to be open too.

So a player who wants to host normally forwards a port and hopes their router
cooperates. The launcher removes both requirements by owning both halves before
the game is ever advertised, without touching PvPGN and without rewriting a single
packet.

## 2. The mechanism

Two independent fixes, one for each half of the advertised address.

**The IP half: tunnel the realm connection.** The launcher points WC3's
Battle.net gateway at `127.0.0.1` (`client.Configure` writes the gateway list;
`hostGatewayHost = "127.0.0.1"` in `config.go`) and runs a local proxy there
(`internal/bnetgateway/gateway.go`) listening on the hardwired bnet port 6112.
That proxy doesn't talk to PvPGN itself. It hands each WC3 connection to the relay
over a single outbound TLS tunnel (`internal/relaylink`), and the relay
(`internal/relay`), which lives on the server next to PvPGN, dials PvPGN for it
(`Session.openPvpgn`). So the BNCS connection reaches PvPGN from the **server's**
side, not from the player's home NAT. PvPGN reads the game host address off that
server-side connection, so the advertised IP is the server's, which every joiner
can already reach.

The proxy is a plain byte pump. It does not parse or rewrite the stream (see
section 5 for why that matters). The address gets pinned by *where the connection
comes from*, not by editing what the connection says.

**The port half: declare the relay's port.** When the tunnel comes up, the relay
allocates a public port `P` from a forwarded pool (`internal/relay/pool.go`,
default 6200-6299) and returns it in the HELLO_ACK. The launcher writes that `P`
into WC3's own `netgameport` registry value **before launch**
(`client.SetGamePort`), so WC3 itself declares `P` in `SID_NETGAMEPORT`. WC3 also
listens for joiners on `127.0.0.1:P` locally; the local gateway keeps 6112.

**Joiner splicing.** The relay binds `0.0.0.0:P` on the server
(`Session.Run` -> `pubLn`). A joiner connecting to `<server>:P` lands there. The
relay assigns the joiner an even stream id and sends an OPEN down the tunnel
(`Session.acceptJoiners`); the launcher answers by dialing WC3's local listener at
`127.0.0.1:P` and splicing the joiner's bytes to it both ways
(`Link.bridgeJoiner`). To the joiner it's a direct connection to the host; the
server and the tunnel are invisible.

Net effect: PvPGN advertises `<server>:P`, joiners hit the server on `P`, and their
traffic is carried back to the host over the same tunnel the host already opened
outbound. Nobody forwarded a port.

```
  host player's machine                 server (PvPGN + relay)            joiner
  =====================                 ======================            ======

  WC3 --realm--> local gateway :6112
                 (byte pump) |
                             | odd stream (per WC3->PvPGN conn)
                             v
                       relaylink ===== one outbound TLS tunnel =====> relay session
                             ^                                            | dials
                             |                                            v
                             |                                          PvPGN :6112
                             |                                  (sees server-side peer,
                             |                                   advertises <server>:P)
                             |
                             | even stream (per joiner)
                             |<=================================== relay binds :P <-- TCP -- WC3
   WC3 listener 127.0.0.1:P -+              splice                  on the server         (joiner)
   (netgameport = P)
```

Everything the launcher writes (`gateway = 127.0.0.1`, `netgameport = P`) is set
before the game starts. Once WC3 is running, the launcher just carries the tunnel
and exits when the game process closes (`client.WaitForGameExit`, watching the
real `war3.exe`, not the loader).

## 3. The tunnel wire protocol

One TLS connection carries the host's whole session: the realm login, the version
checks, and every joiner. It's multiplexed by a tiny framing layer in
`internal/tunnel/frame.go`.

**Frame header (6 bytes, `HeaderLen`):**

    [0xF9 magic][type][uint16 LE total length incl header][uint16 LE stream id][payload]

`0xF9` (`Magic`) is the first byte of every frame, chosen to sit alongside aura's
W3GS `0xF7` / GPS `0xF8`, which share the `[magic][type][uint16 len]` shape. A read
that sees any other first byte means the stream desynced and `ReadFrame` errors out
rather than guessing.

**Frame types:**

| Type | Name | Direction | Meaning |
|------|------|-----------|---------|
| 1 | HELLO | launcher -> relay | token, opens the session |
| 2 | HELLO_ACK | relay -> launcher | allocated port `P` (uint16 LE) + public IP |
| 3 | OPEN | both | open a new stream (see parity below) |
| 4 | DATA | both | opaque bytes for a stream |
| 5 | CLOSE | both | stream ended |
| 6 / 7 | PING / PONG | both | keepalive |
| 8 | GAME_OVER | launcher -> relay | game ended, release `P` |
| 9 | ERROR | relay -> launcher | e.g. `pool_full`, `unauthorized` |

**Stream multiplexing by id parity.** Stream 0 is control (HELLO, ACK, PING, PONG,
GAME_OVER, ERROR). Data streams split by who opens them:

- **Odd ids (1, 3, 5, ...)** are launcher-initiated, one per WC3 -> PvPGN
  connection. A native login opens several: the realm connection plus the BNFTP
  version-check transfers. `Link.OpenClientStream` allocates the next odd id and
  sends OPEN; the relay answers by dialing PvPGN for that stream
  (`Session.openPvpgn`). Stream 1 is the host's realm connection.
- **Even ids (2, 4, 6, ...)** are relay-initiated, one per joiner. The relay
  allocates the next free even id and sends OPEN; the launcher answers by dialing
  WC3's local listener (`Link.bridgeJoiner`).

OPEN is used in both directions, and the id's parity is what says which side
initiated and therefore what the stream carries. The split lets both ends open
streams concurrently without ever colliding on an id. DATA payloads are never
parsed by the tunnel: odd streams carry opaque BNCS, even streams carry opaque
W3GS. A socket read larger than `MaxPayload` (65535 minus the header) is chunked
across DATA frames by the sender (`sendData` on both sides) and reassembled by the
byte stream on the other end.

**TLS detection by first-byte peek.** The relay accepts on one port and tells a TLS
client from a plaintext one by peeking a single byte (`Server.wrapTLS`): `0x16` is
a TLS handshake record, `0xF9` is a plaintext tunnel frame. A `prefixConn` re-serves
that peeked byte so neither the TLS handshake nor the first frame loses it. The
launcher always speaks TLS (`relaylink.Dial` uses `tls.Dialer`, no plaintext path);
the plaintext branch on the server exists only as a rollout bridge and is refused
outright once `-require-tls` is set.

**Backpressure.** Each stream has a bounded queue on both ends (`inboxCap` /
`connOutCap`, 512 chunks). If a consumer falls that far behind, e.g. a joiner on
bad wifi, that one stream is dropped instead of stalling the shared demux loop and
freezing every other stream, including the host's realm connection
(`stream.deliver` on the launcher, `relayConn.deliver` on the relay). One slow peer
can't head-of-line-block the session.

## 4. Security model

The tunnel port faces the internet, so the relay is built to survive hostile input
before a session proves anything (`internal/relay/server.go`, `session.go`):

- **TLS only.** The tunnel carries the realm session end to end, so it's encrypted.
  The relay terminates TLS (`wrapTLS`); with `-require-tls` there's no plaintext
  path. The launcher pins the relay's certificate by its SubjectPublicKeyInfo
  SHA-256 (`relayTLSConfig` in `main.go`), so a self-signed cert is enough and no
  public CA is trusted. The pin is public information and safe to ship in the
  binary.
- **Shared token gate.** The launcher presents a token in HELLO
  (`Session.Run`). It gates "must be our launcher", not per-user identity, PvPGN's
  account login is the real play-access auth. `-require-auth` rejects a bad or
  absent token; the relay refuses to start with `-require-auth` and no token, since
  that would reject every launcher and take the realm down.
- **Session caps, independent of auth.** Per-IP (4) and global (80) session caps
  (`Server.admit`) apply *before* HELLO, so an attacker who never sends a valid
  token still can't make the relay allocate ports or FDs without bound. Auth
  decides who gets in; the caps decide how much anyone can take.
- **Per-game caps.** 32 joiners per game (a real lobby is <=24) and 16
  launcher-opened streams per session (`maxJoinersPerGame`, `maxClientStreams`), so
  a flood on a public port or a malicious tunnel can't exhaust host-side dials.
- **Timeouts.** One absolute pre-HELLO budget covers the peek, the TLS handshake,
  and the HELLO frame (`helloTimeout`, 10s), so a slowloris gets that total, not
  per step. After HELLO a rolling idle deadline (`idleTimeout`, 180s, re-armed per
  frame) tears down a tunnel that goes silent so it can't sit on a pool port
  forever.
- **Bounded queues.** The per-stream outbound queues above also mean a stalled peer
  can't consume unbounded memory.

**Secrets and endpoint are build-injected and absent from source.** The repo
carries no server address and no token. `serverHost` defaults to `wc3.example.com`,
`relayToken` and `relayCertPin` are empty (`version.go`), all injected at build time
with `-ldflags "-X main..."`. A build with the defaults can't reach a real server;
publishing an endpoint in source would just invite everyone who reads it to point
traffic at it, so the people who should reach it get a built binary instead.

## 5. The approach we didn't need

The first working design pinned the address by **packet surgery** on the BNCS
stream, and it's the more interesting story, so it's worth keeping as a footnote.

The plan, verified line by line against PvPGN 1.99.7.2.1 source, was to sit in the
middle of the realm connection and edit three things on the way past:

1. **Drop the client's `SID_CLIENT_UNKNOWN_1B` (0x1b).** The retail client derives
   this packet from its own UDP self-test and uses it to advertise the address it
   thinks it's reachable at, which behind NAT is the unreachable home WAN address.
2. **Inject our own `0x1b`** carrying the relay's address, before
   `SID_STARTADVEX3` (0x1c, the game-creation packet).
3. **Overwrite `SID_NETGAMEPORT` (0x45)** with the relay's port.

That's roughly 400 lines of stateful BNCS parsing, and it worked. Then the relay
made all of it unnecessary. Once the realm connection is *tunneled* so it reaches
PvPGN from the server (section 2), PvPGN reads the server's address off the socket
for free, no `0x1b` surgery needed. And writing `netgameport` into the client's own
config before launch (section 2) makes the client declare the right port itself, no
`0x45` rewrite needed. So the whole MITM layer got deleted.

The one scar it left is load-bearing and worth understanding:
`internal/bnetgateway/gateway.go` copies the stream **verbatim**, and the comment
there explains why parsing it would be a bug, not just unnecessary. WC3 opens the
realm socket with a lone `0x01` protocol-selector byte that is **not** part of the
length-framed BNCS packet stream that follows. Anything that tries to parse the
session as packets desyncs on that very first byte and the login never completes.
The byte pump is not laziness; it's the only correct thing to do once you're not
rewriting anything. Deleting the packet surgery is what made the byte pump possible.

## 6. Platform notes

The same binary runs on Windows natively and on Linux through Wine
(`internal/client`).

**Windows** writes directly to the real `HKEY_CURRENT_USER` registry, no admin
rights needed (`client_windows.go`): the gateway list as a `REG_MULTI_SZ`,
`netgameport` as a `REG_DWORD`, then launches `w3l.exe` directly. WC3 renders
Direct3D 8 through the bundled `d3d8to9` forwarding to the system's native
Direct3D 9, so the Windows download stays small (`bundle_windows.go`).

**Linux** runs everything in a **dedicated Wine prefix inside the game folder**
(`winePrefix` in `client_linux.go`), so the user's own `~/.wine`, packages, and
settings are never touched. Existing Wine on the system is used exactly as-is; if
Wine is missing the launcher detects the distro from `/etc/os-release` and offers
the right install command with consent, never editing package config on the user's
behalf, and shows (rather than runs) the command on immutable/ostree systems
(`prereqs_linux.go`). The registry writes go through `wine reg add` deliberately:
`reg add` stores the value as single-byte ANSI, which is what WC3's `Game.dll`
reads back with the ANSI registry API; a UTF-16 value is read as a one-character
"w" and the gateway silently never appears.

Two Wine-specific details:

- **DXVK.** WC3's Direct3D 8 is forwarded to Direct3D 9 and then through the
  bundled DXVK `d3d9.dll` to Vulkan (`WINEDLLOVERRIDES=d3d8=n,d3d9=n`). Wine's
  default D3D-to-OpenGL path fails to choose a pixel format on NVIDIA/Wayland and
  crashes the game on startup; DXVK is reliable. The Linux bundle carries this DLL;
  the Windows bundle deliberately omits it (`bundle_linux.go` vs
  `bundle_windows.go`).
- **Virtual desktop.** The game runs inside a `wine explorer /desktop` window sized
  to the display. That fixes two Wayland/Wine problems at once: an
  exclusive-fullscreen game can't be alt-tabbed back into, and its in-game
  resolution change doesn't take effect. `WC3_FULLSCREEN=1` opts back into raw
  fullscreen.

**The W3L loader** (`w3l.exe`, pinned in `config.go` as the bundle) is what lets a
classic client talk to PvPGN at all; stock WC3 refuses to connect to a non-Blizzard
realm without it. The game client itself is never redistributed here: the launcher
fetches it from Blizzard's own official free installer
(`installer.Download` / `blizzardInstaller` in `config.go`) and only ships the
open-source loader and community maps. The realm requires patch 1.28.5; Reforged
(1.29+) can't connect to a classic PvPGN server, so the bundle pins that level.
