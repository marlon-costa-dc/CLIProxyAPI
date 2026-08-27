package config

import "testing"

func TestParseConfigBytesValidatesModelPricingProvenance(t *testing.T) {
	_, err := ParseConfigBytes([]byte(`
openai-compatibility:
  - name: provider
    base-url: https://example.invalid/v1
    models:
      - name: model
        alias: model
        pricing:
          input-per-million: 1
          output-per-million: 2
          currency: USD
          source: models.dev
          source-digest: sha256:abc
          fetched-at: 2026-08-27T12:00:00Z
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
}

func TestParseConfigBytesRejectsIncompleteModelPricing(t *testing.T) {
	_, err := ParseConfigBytes([]byte(`
openai-compatibility:
  - name: provider
    base-url: https://example.invalid/v1
    models:
      - name: model
        alias: model
        pricing:
          input-per-million: 1
          currency: USD
`))
	if err == nil {
		t.Fatal("ParseConfigBytes() accepted pricing without output/provenance")
	}
}
