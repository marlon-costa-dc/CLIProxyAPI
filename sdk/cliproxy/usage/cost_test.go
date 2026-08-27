package usage

import "testing"

func TestCalculateCostUsesOnlyPublishedComponents(t *testing.T) {
	tokens := NewSubsetTokenBreakdown(1_000_000, 250_000, 0, 500_000, 0, 1_500_000)
	cost := CalculateCost(tokens, 2, 8, nil, "models.dev", "sha256:abc")
	if cost.Quality != CostQualityPartial {
		t.Fatalf("quality = %q, want partial", cost.Quality)
	}
	if cost.Input != 1.5 || cost.CacheRead != 0 || cost.Output != 4 || cost.EstimatedTotal != 5.5 {
		t.Fatalf("cost = %+v, want known components only", cost)
	}
}

func TestCalculateCostPreservesExplicitFreeCachePrice(t *testing.T) {
	cachePrice := 0.0
	tokens := NewSubsetTokenBreakdown(1_000_000, 250_000, 0, 500_000, 0, 1_500_000)
	cost := CalculateCost(tokens, 2, 8, &cachePrice, "models.dev", "sha256:abc")
	if cost.Quality != CostQualityComplete || cost.EstimatedTotal != 5.5 {
		t.Fatalf("cost = %+v, want complete explicit free cache pricing", cost)
	}
}
