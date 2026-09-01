package modelrouting

import (
	"errors"
	"strings"
	"testing"
)

func transitionIdentity(generation uint64, suffix byte) *ActiveIdentityV2 {
	value := "sha256:" + strings.Repeat(string(suffix), 64)
	return &ActiveIdentityV2{
		Generation: generation, SnapshotDigest: value, ProjectionDigest: value, ConfigDigest: value,
	}
}

func TestValidateTransitionEnforcesBootstrapAndExactCAS(t *testing.T) {
	t.Parallel()
	current := transitionIdentity(4, 'a')
	expected := *current
	stale := *current
	stale.ConfigDigest = transitionIdentity(4, 'b').ConfigDigest

	tests := []struct {
		name     string
		current  *ActiveIdentityV2
		expected *ActiveIdentityV2
		next     *Config
		want     error
	}{
		{name: "bootstrap", next: &Config{Generation: 1}},
		{name: "bootstrap requires generation one", next: &Config{Generation: 2}, want: ErrInvalidPublication},
		{name: "steady state requires expected identity", current: current, next: &Config{Generation: 5}, want: ErrCASConflict},
		{name: "steady state rejects stale identity", current: current, expected: &stale, next: &Config{Generation: 5}, want: ErrCASConflict},
		{name: "steady state rejects skipped generation", current: current, expected: &expected, next: &Config{Generation: 6}, want: ErrCASConflict},
		{name: "steady state exact successor", current: current, expected: &expected, next: &Config{Generation: 5}},
		{name: "active projection cannot be removed", current: current, expected: &expected, want: ErrInvalidPublication},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTransition(test.current, test.expected, test.next)
			if test.want == nil && err != nil {
				t.Fatalf("ValidateTransition() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("ValidateTransition() error = %v, want %v", err, test.want)
			}
		})
	}
}
