package config

import (
	"errors"
	"fmt"
	"strings"
)

type runtimeConfigValidationError struct {
	domain string
	err    error
}

func (e *runtimeConfigValidationError) Error() string { return e.err.Error() }
func (e *runtimeConfigValidationError) Unwrap() error { return e.err }

// RuntimeConfigValidationDomain identifies which canonical config contract failed.
func RuntimeConfigValidationDomain(err error) string {
	var validationError *runtimeConfigValidationError
	if errors.As(err, &validationError) {
		return validationError.domain
	}
	return "runtime config"
}

// ValidateRuntimeConfig validates contracts required by every config ingress.
func (cfg *Config) ValidateRuntimeConfig() error {
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return &runtimeConfigValidationError{domain: "credential weights", err: errValidate}
	}
	if errValidate := cfg.ValidateModelPricing(); errValidate != nil {
		return &runtimeConfigValidationError{domain: "model pricing", err: errValidate}
	}
	return nil
}

// ValidateModelPricing rejects incomplete or ambiguous external pricing records.
func (cfg *Config) ValidateModelPricing() error {
	if cfg == nil {
		return nil
	}
	for providerIndex := range cfg.OpenAICompatibility {
		for modelIndex := range cfg.OpenAICompatibility[providerIndex].Models {
			pricing := cfg.OpenAICompatibility[providerIndex].Models[modelIndex].Pricing
			if pricing == nil {
				continue
			}
			prefix := fmt.Sprintf("openai-compatibility[%d].models[%d].pricing", providerIndex, modelIndex)
			if pricing.InputPerMillion == nil || pricing.OutputPerMillion == nil {
				return fmt.Errorf("%s: input-per-million and output-per-million are required", prefix)
			}
			for name, value := range map[string]*float64{
				"input-per-million":      pricing.InputPerMillion,
				"output-per-million":     pricing.OutputPerMillion,
				"cache-read-per-million": pricing.CacheReadPerMillion,
			} {
				if value != nil && *value < 0 {
					return fmt.Errorf("%s.%s: must be non-negative", prefix, name)
				}
			}
			if strings.ToUpper(strings.TrimSpace(pricing.Currency)) != "USD" {
				return fmt.Errorf("%s.currency: must be USD", prefix)
			}
			if strings.TrimSpace(pricing.Source) == "" || strings.TrimSpace(pricing.SourceDigest) == "" || pricing.FetchedAt.IsZero() {
				return fmt.Errorf("%s: source, source-digest, and fetched-at are required", prefix)
			}
		}
	}
	return nil
}
