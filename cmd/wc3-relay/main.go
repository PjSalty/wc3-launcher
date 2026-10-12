// Command wc3-relay is the server-side native-host relay. It runs on the server
// beside pvpgn+aura and lets a player host a native Create Game with zero router
// config: the player's launcher opens one outbound tunnel here, the relay
// allocates a public port and proxies the player's realm connection to pvpgn
// (so pvpgn advertises this server's IP as the game host), then fans joiner TCP
// down the tunnel to the player's Warcraft III.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"wc3-launcher/internal/relay"
)

// parseSwitch reads a 0/1 setting. Unset means def, so a protection stays on
// unless it is turned off on purpose, and anything else is a typo, not a choice.
func parseSwitch(name, v string, def bool) (bool, error) {
	switch v {
	case "":
		return def, nil
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	}
	return def, fmt.Errorf("%s=%q: want 0 or 1", name, v)
}

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)
	authDefault, err := parseSwitch("RELAY_REQUIRE_AUTH", os.Getenv("RELAY_REQUIRE_AUTH"), true)
	if err != nil {
		logger.Fatalf("relay: %v", err)
	}
	tlsDefault, err := parseSwitch("RELAY_REQUIRE_TLS", os.Getenv("RELAY_REQUIRE_TLS"), true)
	if err != nil {
		logger.Fatalf("relay: %v", err)
	}

	listen := flag.String("listen", ":7000", "tunnel listen address for host launchers")
	pvpgn := flag.String("pvpgn", "127.0.0.1:6112", "pvpgn bnet address to proxy host sessions to")
	publicIP := flag.String("public-ip", "", "public IP advertised to joiners (informational in HELLO_ACK)")
	poolLo := flag.Uint("pool-lo", 6200, "low end of the public game-port pool (must match the forwarded port range)")
	poolHi := flag.Uint("pool-hi", 6299, "high end of the public game-port pool")
	token := flag.String("token", os.Getenv("RELAY_TOKEN"), "shared tunnel token launchers must present")
	requireAuth := flag.Bool("require-auth", authDefault, "reject tunnels that do not present the token (RELAY_REQUIRE_AUTH=0 turns the gate off)")
	tlsCert := flag.String("tls-cert", os.Getenv("RELAY_TLS_CERT"), "path to the TLS certificate (PEM) served on the tunnel port")
	tlsKey := flag.String("tls-key", os.Getenv("RELAY_TLS_KEY"), "path to the TLS private key (PEM)")
	requireTLS := flag.Bool("require-tls", tlsDefault, "reject plaintext tunnels, TLS only (RELAY_REQUIRE_TLS=0 also accepts plaintext)")
	flag.Parse()

	if *poolHi < *poolLo || *poolHi > 65535 {
		logger.Fatalf("relay: invalid pool range %d-%d", *poolLo, *poolHi)
	}

	// Both gates are on unless turned off by name, so a deployment that forgets a
	// setting fails to start instead of running open. The token gate with no token
	// would reject every launcher, so that also refuses to start.
	if *requireAuth && *token == "" {
		logger.Fatalf("relay: no tunnel token: set -token/RELAY_TOKEN, or RELAY_REQUIRE_AUTH=0 to run without the token gate on purpose")
	}
	if *token == "" {
		logger.Printf("relay: WARNING no tunnel token configured, the token gate is OFF (session caps + TLS still apply)")
	} else if !*requireAuth {
		logger.Printf("relay: WARNING token configured but the gate is off, so a wrong token is only logged")
	}
	if !*requireTLS {
		logger.Printf("relay: WARNING plaintext tunnels are accepted (RELAY_REQUIRE_TLS=0)")
	}

	var tlsConfig *tls.Config
	if *tlsCert != "" && *tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			logger.Fatalf("relay: load TLS cert: %v", err)
		}
		// TLS 1.3 only: both ends are ours (the launcher speaks TLS 1.3 too), so
		// there is no legacy client to accommodate, and it drops the whole 1.2
		// downgrade/cipher-negotiation surface.
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
		logger.Printf("relay: TLS enabled (require-tls=%v)", *requireTLS)
	} else if *requireTLS {
		logger.Fatalf("relay: no TLS certificate: set -tls-cert/-tls-key (RELAY_TLS_CERT/RELAY_TLS_KEY), or RELAY_REQUIRE_TLS=0 to accept plaintext on purpose")
	}

	srv := &relay.Server{
		Listen:      *listen,
		Pvpgn:       *pvpgn,
		PublicIP:    *publicIP,
		Pool:        relay.NewPool(uint16(*poolLo), uint16(*poolHi)),
		Logger:      logger,
		Token:       *token,
		RequireAuth: *requireAuth,
		TLSConfig:   tlsConfig,
		RequireTLS:  *requireTLS,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srv.ListenAndServe(ctx); err != nil && ctx.Err() == nil {
		logger.Fatalf("relay: %v", err)
	}
}
