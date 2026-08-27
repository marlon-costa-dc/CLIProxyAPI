package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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
	seen := make(map[string]struct{}, len(cfg.ModelPricing))
	for index := range cfg.ModelPricing {
		entry := &cfg.ModelPricing[index]
		prefix := fmt.Sprintf("model-pricing[%d]", index)
		channel := strings.ToLower(strings.TrimSpace(entry.Channel))
		if channel == "" {
			return fmt.Errorf("%s.channel: is required", prefix)
		}
		modelID := strings.TrimSpace(entry.Model)
		if modelID == "" {
			return fmt.Errorf("%s.model: is required", prefix)
		}
		identity := registry.ModelPricingKey(channel, modelID)
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("%s: duplicate exact channel/model identity %q/%q", prefix, channel, modelID)
		}
		seen[identity] = struct{}{}
		if err := validatePricingRecord(prefix, &entry.ModelPricing); err != nil {
			return err
		}
	}
	for providerIndex := range cfg.OpenAICompatibility {
		for modelIndex := range cfg.OpenAICompatibility[providerIndex].Models {
			pricing := cfg.OpenAICompatibility[providerIndex].Models[modelIndex].Pricing
			if pricing == nil {
				continue
			}
			prefix := fmt.Sprintf("openai-compatibility[%d].models[%d].pricing", providerIndex, modelIndex)
			if err := validatePricingRecord(prefix, pricing); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePricingRecord(prefix string, record *registry.ModelPricing) error {
	if record == nil {
		return fmt.Errorf("%s: pricing record is required", prefix)
	}
	if record.InputPerMillion == nil || record.OutputPerMillion == nil {
		return fmt.Errorf("%s: input-per-million and output-per-million are required", prefix)
	}
	for name, value := range map[string]*float64{
		"input-per-million": record.InputPerMillion, "output-per-million": record.OutputPerMillion, "cache-read-per-million": record.CacheReadPerMillion,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%s.%s: must be non-negative", prefix, name)
		}
	}
	if strings.ToUpper(strings.TrimSpace(record.Currency)) != "USD" {
		return fmt.Errorf("%s.currency: must be USD", prefix)
	}
	if strings.TrimSpace(record.Source) == "" || strings.TrimSpace(record.SourceDigest) == "" || record.FetchedAt.IsZero() {
		return fmt.Errorf("%s: source, source-digest, and fetched-at are required", prefix)
	}
	return nil
}

// ModelPricingMap returns a defensive exact-ID catalog for the runtime registry.
func (cfg *Config) ModelPricingMap() map[string]*registry.ModelPricing {
	result := make(map[string]*registry.ModelPricing, len(cfg.ModelPricing))
	for index := range cfg.ModelPricing {
		entry := &cfg.ModelPricing[index]
		record := entry.ModelPricing
		result[registry.ModelPricingKey(entry.Channel, entry.Model)] = &record
	}
	return result
}
