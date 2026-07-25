package rcon

import (
	"reflect"
	"testing"
)

func TestParsePlayers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "Online players (0):", nil},
		{
			"single line comma",
			"Online players (2): alice, bob",
			[]string{"alice", "bob"},
		},
		{
			"multiline online suffix",
			"Online players (2):\n  alice (online)\n  bob (online)\n",
			[]string{"alice", "bob"},
		},
		{
			"multiline no suffix",
			"Online players (1):\n  charlie\n",
			[]string{"charlie"},
		},
		{"unrelated", "some other response", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePlayers(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parsePlayers(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
