//go:build linux

package client

import "testing"

func TestParseWineGateway(t *testing.T) {
	// Exact shape of `wine reg query ... /v "Battle.net Gateways"` output for a
	// realm the launcher wrote (entries separated by a literal `\0`).
	out := `    Battle.net Gateways    REG_MULTI_SZ    1001\00\0lintest.example.com\0-6\0LinuxRealm\0uswest.battle.net\08\0Lordaeron`
	host, name, ok := parseWineGateway(out)
	if !ok || host != "lintest.example.com" || name != "LinuxRealm" {
		t.Fatalf("parseWineGateway = %q/%q/%v, want lintest.example.com/LinuxRealm/true", host, name, ok)
	}

	// A stock (no custom realm) gateway must not migrate.
	if _, _, ok := parseWineGateway(`x    REG_MULTI_SZ    1001\00\0uswest.battle.net\08\0Lordaeron`); ok {
		t.Fatal("a stock battle.net gateway should not migrate")
	}
	// No value present -> nothing to migrate.
	if _, _, ok := parseWineGateway("ERROR: The system was unable to find the specified registry key or value."); ok {
		t.Fatal("missing REG_MULTI_SZ must return ok=false")
	}
}
