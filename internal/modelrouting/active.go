package modelrouting

import (
	"fmt"
	"sync/atomic"
	"time"
)

var activeStateValue atomic.Pointer[activeState]

type activeState struct {
	receipt ActiveProjection
	runtime RuntimeProjection
}

// RuntimeProjection is the minimal immutable projection shared with model
// listings and usage accounting.
type RuntimeProjection struct {
	PricingByRoute map[string]*Pricing
}

// Activate publishes a receipt only after the full projection has been applied.
func Activate(cfg *Config, loadedAt time.Time) {
	if cfg == nil {
		activeStateValue.Store(nil)
		return
	}
	runtime := &RuntimeProjection{PricingByRoute: make(map[string]*Pricing)}
	for _, model := range cfg.DirectModels {
		for _, route := range model.Routes {
			runtime.PricingByRoute[route.RouteKey.RouteChannel+"\x00"+route.RuntimeModelID] = ClonePricing(route.Pricing)
		}
	}
	activeStateValue.Store(&activeState{
		receipt: ActiveProjection{
			Generation:       cfg.Generation,
			SnapshotDigest:   cfg.SnapshotDigest,
			ProjectionDigest: cfg.ProjectionDigest,
			LoadedAt:         loadedAt.UTC(),
		},
		runtime: *runtime,
	})
}

// ActivePricing returns a defensive exact-decimal price record for one route.
func ActivePricing(routeChannel, runtimeModelID string) *Pricing {
	state := activeStateValue.Load()
	if state == nil {
		return nil
	}
	return ClonePricing(state.runtime.PricingByRoute[routeChannel+"\x00"+runtimeModelID])
}

// ClonePricing deep-copies an optional price record.
func ClonePricing(pricing *Pricing) *Pricing {
	if pricing == nil {
		return nil
	}
	copyPricing := *pricing
	copyPricing.Entries = make([]PricingEntry, len(pricing.Entries))
	for index, entry := range pricing.Entries {
		copyPricing.Entries[index] = entry
		copyPricing.Entries[index].TierType = cloneOptional(entry.TierType)
		copyPricing.Entries[index].TierSize = cloneOptional(entry.TierSize)
		copyPricing.Entries[index].ContextKey = cloneOptional(entry.ContextKey)
	}
	return &copyPricing
}

func cloneOptional[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

// ValidateTransition rejects stale or conflicting generations while allowing
// an idempotent replay of the exact active projection.
func ValidateTransition(current *ActiveProjection, next *Config) error {
	if current != nil && next == nil {
		return fmt.Errorf("model-routing: active projection cannot be removed through an unversioned config update")
	}
	if next == nil || current == nil {
		return nil
	}
	if next.Generation < current.Generation {
		return fmt.Errorf("model-routing.generation: stale generation %d; active is %d", next.Generation, current.Generation)
	}
	if next.Generation == current.Generation &&
		(next.SnapshotDigest != current.SnapshotDigest || next.ProjectionDigest != current.ProjectionDigest) {
		return fmt.Errorf("model-routing.generation: generation %d conflicts with active digests", next.Generation)
	}
	return nil
}

// Active returns a defensive copy of the active digest receipt.
func Active() *ActiveProjection {
	state := activeStateValue.Load()
	if state == nil {
		return nil
	}
	copyValue := state.receipt
	return &copyValue
}
