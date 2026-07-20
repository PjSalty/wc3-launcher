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
	"log"
	"os"
	"os/signal"
	"syscall"

	"wc3-launcher/internal/relay"
)

func main() {
	listen := flag.String("listen", ":7000", "tunnel listen address for host launchers")
	pvpgn := flag.String("pvpgn", "127.0.0.1:6112", "pvpgn bnet address to proxy host sessions to")
	publicIP := flag.String("public-ip", "", "public IP advertised to joiners (informational in HELLO_ACK)")
	poolLo := flag.Uint("pool-lo", 6200, "low end of the public game-port pool (must match the forwarded port range)")
	poolHi := flag.Uint("pool-hi", 6299, "high end of the public game-port pool")
	token := flag.String("token", os.Getenv("RELAY_TOKEN"), "shared tunnel token launchers must present (empty = accept any)")
	requireAuth := flag.Bool("require-auth", os.Getenv("RELAY_REQUIRE_AUTH") == "1", "reject tunnels that do not present the token (default: grace mode, log only)")
	tlsCert := flag.String("tls-cert", os.Getenv("RELAY_TLS_CERT"), "path to the TLS certificate (PEM) served on the tunnel port")
	tlsKey := flag.String("tls-key", os.Getenv("RELAY_TLS_KEY"), "path to the TLS private key (PEM)")
	requireTLS := flag.Bool("require-tls", os.Getenv("RELAY_REQUIRE_TLS") == "1", "reject plaintext tunnels, TLS only (default: also accept plain during rollout)")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)
	if *poolHi < *poolLo || *poolHi > 65535 {
		logger.Fatalf("relay: invalid pool range %d-%d", *poolLo, *poolHi)
	}

	// Make the enforcement state impossible to misread. -require-auth with no
	// token would reject every launcher (total self-inflicted outage), so refuse
	// to start rather than take the whole realm down.
	if *requireAuth && *token == "" {
		logger.Fatalf("relay: -require-auth set but no -token/RELAY_TOKEN given; every launcher would be rejected")
	}
	if *token == "" {
		logger.Printf("relay: WARNING no tunnel token configured, the token gate is OFF (session caps + TLS still apply)")
	} else if !*requireAuth {
		logger.Printf("relay: WARNING token configured but -require-auth is off, so the token gate is NOT enforced (grace mode)")
	}
	if !*requireTLS {
		logger.Printf("relay: WARNING -require-tls is off, plaintext tunnels are still accepted (rollout bridge)")
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
		logger.Fatalf("relay: -require-tls set but no -tls-cert/-tls-key provided")
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
