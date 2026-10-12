package main

import "testing"

func TestParseSwitch(t *testing.T) {
	cases := []struct {
		v       string
		def     bool
		want    bool
		wantErr bool
	}{
		{"", true, true, false}, // unset keeps the protection on
		{"", false, false, false},
		{"1", false, true, false},
		{"true", false, true, false},
		{"0", true, false, false}, // off only when asked for by name
		{"false", true, false, false},
		{"yes", true, true, true}, // a typo is an error, never a choice
		{"01", true, true, true},
	}
	for _, c := range cases {
		got, err := parseSwitch("X", c.v, c.def)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("parseSwitch(%q, def=%v) = %v, err=%v; want %v, err=%v", c.v, c.def, got, err, c.want, c.wantErr)
		}
	}
}
