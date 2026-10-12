package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"wc3-launcher/internal/relaylink"
)

// Every failed relay dial used to become errRelayUnreachable, so a certificate
// that did not match the pin and a refused token both printed "relay unavailable"
// and carried on over the unencrypted direct connection.
func TestClassifyRelayError(t *testing.T) {
	fallsBack := func(err error) bool {
		return errors.Is(err, errRelayUnreachable) || errors.Is(err, errRelayTokenRefused) || errors.Is(err, relaylink.ErrBadPort)
	}

	untrusted := classifyRelayError(fmt.Errorf("dial: %w", relaylink.ErrUntrusted))
	if fallsBack(untrusted) {
		t.Errorf("a rejected certificate must stop the launcher, got a fallback error: %v", untrusted)
	}
	if !strings.Contains(untrusted.Error(), "--direct") {
		t.Errorf("the certificate error should name the way to proceed on purpose: %v", untrusted)
	}

	if err := classifyRelayError(&relaylink.RefusedError{Reason: "unauthorized"}); !errors.Is(err, errRelayTokenRefused) {
		t.Errorf("a refused token: got %v, want errRelayTokenRefused", err)
	}
	if err := classifyRelayError(&relaylink.RefusedError{Reason: "pool_full"}); !errors.Is(err, errRelayUnreachable) {
		t.Errorf("a full relay: got %v, want errRelayUnreachable", err)
	}
	if err := classifyRelayError(fmt.Errorf("%w: port 22", relaylink.ErrBadPort)); !errors.Is(err, relaylink.ErrBadPort) || errors.Is(err, errRelayUnreachable) {
		t.Errorf("an unusable port: got %v, want ErrBadPort kept as is", err)
	}
	if err := classifyRelayError(errors.New("connection refused")); !errors.Is(err, errRelayUnreachable) {
		t.Errorf("a plain network failure: got %v, want errRelayUnreachable", err)
	}
}

func TestPinMismatchIsReportedAsUntrusted(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)

	old := relayCertPin
	defer func() { relayCertPin = old }()

	relayCertPin = base64.StdEncoding.EncodeToString(sum[:])
	c, err := tls.Dial("tcp", addr, relayTLSConfig("127.0.0.1"))
	if err != nil {
		t.Fatalf("the server's own key must satisfy its pin: %v", err)
	}
	c.Close()

	relayCertPin = base64.StdEncoding.EncodeToString(make([]byte, sha256.Size))
	if _, err := tls.Dial("tcp", addr, relayTLSConfig("127.0.0.1")); !errors.Is(err, relaylink.ErrUntrusted) {
		t.Fatalf("a key that does not match the pin: got %v, want ErrUntrusted", err)
	}
}
