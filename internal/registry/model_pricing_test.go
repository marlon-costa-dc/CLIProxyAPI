package registry

import (
	"testing"
	"time"
)

func TestOpenAIModelListingIncludesPricingProvenance(t *testing.T) {
	input := 1.25
	output := 5.0
	model := (&ModelRegistry{}).convertModelToMap(&ModelInfo{
		ID: "model",
		Pricing: &ModelPricing{
			InputPerMillion:  &input,
			OutputPerMillion: &output,
			Currency:         "USD",
			Source:           "models.dev",
			SourceDigest:     "sha256:abc",
			FetchedAt:        time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		},
	}, "openai")
	pricing, ok := model["pricing"].(map[string]any)
	if !ok || pricing["input_per_million"] != input || pricing["source_digest"] != "sha256:abc" {
		t.Fatalf("pricing = %#v, want source-attributed listing metadata", model["pricing"])
	}
}
