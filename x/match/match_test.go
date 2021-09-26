package match_test

import (
	"testing"

	"github.com/tendermint/tendermint/x/match"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, input string
		want           bool
	}{
		{"foo", "foo", true},   // exact match
		{"foo", "bar", false},  // no match
		{"foo", "fool", false}, // matches must consume the input
		{"foo*", "foo", true},  // star matches empty
		{"ap*e", "ape", true},
		{"foo*", "fool", true}, // star matches nonempty
		{"ap*e", "apple", true},
		{"", "", true},          // empty matches empty
		{"", "anything", false}, // empty does not match nonempty
		{"*", "", true},         // star matches nothing
		{"*", "aything", true},  // star matches anything
		{"x?y", "x?y", true},    // metacharacters don't confuse us
		{"x..*", "x..y", true},
		{"x..*", "xaby", false},
	}
	for _, test := range tests {
		mfunc := match.Glob(test.pattern)
		if got := mfunc(test.input); got != test.want {
			t.Errorf("Match pattern %q with input %q: got %v, want %v",
				test.pattern, test.input, got, test.want)
		}
	}
}
