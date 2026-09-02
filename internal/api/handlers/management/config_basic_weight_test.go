package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeRoutingStrategyClosestToReset(t *testing.T) {
	for _, input := range []string{"closest-to-reset", "closesttoreset", "ctr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "closest-to-reset" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want closest-to-reset, true", input, got, ok)
		}
	}
}
