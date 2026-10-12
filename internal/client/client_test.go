package client

import (
	"strconv"
	"testing"
)

// TestGatewayStrings verifies the Battle.net gateway value is in the exact
// positional layout WC3's parser needs: a "1001" version header, a "0" selected
// index, then {address, zone, name} triples with no per-entry index, our realm
// first.
func TestGatewayStrings(t *testing.T) {
	got := gatewayStrings("wc3.example.com", "-6", "PvPGN")

	// 2 header tokens + 3 fields per gateway (our realm + the classic four).
	if want := 2 + 3*(1+len(wc3DefaultGateways)); len(got) != want {
		t.Fatalf("gatewayStrings len = %d, want %d: %v", len(got), want, got)
	}
	// token[0] must be an integer >= 1001 or WC3 rejects the whole list.
	if v, err := strconv.Atoi(got[0]); err != nil || v < 1001 {
		t.Fatalf("token[0] = %q, want an integer >= 1001", got[0])
	}
	// token[1] is the selected-gateway index; our realm is entry 0.
	if got[1] != "0" {
		t.Fatalf("token[1] = %q, want selected index \"0\"", got[1])
	}
	// First gateway triple is our realm.
	if got[2] != "wc3.example.com" || got[3] != "-6" || got[4] != "PvPGN" {
		t.Fatalf("first gateway = %v, want [wc3.example.com -6 PvPGN]", got[2:5])
	}
	// After the two header tokens the stream is strict {address, zone, name}
	// triples: each address field must be a hostname, not a bare number, or the
	// positional layout has desynced.
	for i := 2; i < len(got); i += 3 {
		if _, err := strconv.Atoi(got[i]); err == nil {
			t.Fatalf("address field %q at index %d is a bare number, layout desynced", got[i], i)
		}
	}
}

// In relay mode the launcher writes 127.0.0.1 as the gateway host, because WC3
// talks to the local gateway. Migration used to adopt that as "the server" and
// save it for good.
func TestParseGatewayNeverAdoptsThisMachine(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.0.0.53", "localhost", "LOCALHOST", "::1"} {
		if _, _, ok := parseGateway([]string{"1001", "0", host, "-6", "PvPGN"}); ok {
			t.Errorf("parseGateway adopted %q as a server to migrate", host)
		}
	}
	if h, n, ok := parseGateway([]string{"1001", "0", "realm.example.net", "-6", "MyRealm"}); !ok || h != "realm.example.net" || n != "MyRealm" {
		t.Errorf("a real server must still migrate, got %q/%q/%v", h, n, ok)
	}
}
