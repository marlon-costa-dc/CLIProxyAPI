package modelrouting

import (
	"errors"
	"fmt"
)

var (
	ErrCASConflict        = errors.New("model-routing compare-and-swap conflict")
	ErrInvalidPublication = errors.New("invalid model-routing publication")
)

// ValidateTransition enforces compare-and-swap and exact monotonic generation.
// Bootstrap is permitted only from an empty state with generation one.
func ValidateTransition(current, expected *ActiveIdentityV2, next *Config) error {
	if next == nil {
		return fmt.Errorf("%w: an active projection cannot be removed", ErrInvalidPublication)
	}
	if current == nil {
		if expected != nil {
			return fmt.Errorf("%w: bootstrap expected an existing active identity", ErrCASConflict)
		}
		if next.Generation != 1 {
			return fmt.Errorf("%w: bootstrap generation must be 1, got %d", ErrInvalidPublication, next.Generation)
		}
		return nil
	}
	if expected == nil {
		return fmt.Errorf("%w: steady-state publication requires the complete expected active identity", ErrCASConflict)
	}
	if *current != *expected {
		return fmt.Errorf("%w: expected active identity does not match the current identity", ErrCASConflict)
	}
	if next.Generation != current.Generation+1 {
		return fmt.Errorf("%w: expected generation %d, got %d", ErrCASConflict, current.Generation+1, next.Generation)
	}
	return nil
}
