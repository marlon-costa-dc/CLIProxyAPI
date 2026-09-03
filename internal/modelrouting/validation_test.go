package modelrouting

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validConfig() Config {
	latency := int64(12)
	variantID := "high"
	variantDisplayName := "High reasoning"
	reasoningOption := "high"
	modelKey := ModelKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4"}
	health := Health{Status: "healthy", Selectable: true, ObservedAt: "2026-08-27T18:45:00Z", LatencyMS: &latency}
	routeKey := RouteKey{ModelKey: modelKey, RouteChannel: "openai"}
	secondRouteKey := RouteKey{ModelKey: modelKey, RouteChannel: "claude"}
	pricing := &Pricing{
		Currency: "USD",
		Unit:     "per_million_tokens",
		SourceID: "models_dev",
		Entries: []PricingEntry{
			{Name: "input", Amount: "2.5000000000000000001"},
			{Name: "output", Amount: "15"},
		},
	}
	cfg := Config{
		SchemaVersion:    2,
		Generation:       42,
		SnapshotDigest:   testDigest,
		ProjectionDigest: testDigest,
		DirectModels: []DirectModel{{
			ModelKey: modelKey, DisplayName: "GPT-5.4", Active: true,
			Variants: []Variant{{
				VariantKey:      VariantKey{ModelKey: modelKey, VariantID: variantID},
				DisplayName:     &variantDisplayName,
				ReasoningOption: &reasoningOption,
				Protocols:       []string{"openai_chat"},
			}},
			Routes: []DirectRoute{{
				RouteKey:               routeKey,
				CatalogRouteProviderID: "openrouter", CatalogRouteModelID: "openai/gpt-5.4", RuntimeModelID: "gpt-5.4", RouteSelector: SelectorForRoute(routeKey, "gpt-5.4"),
				QuotaDomains: []string{"openai-primary"}, Protocols: []string{"openai_chat"},
				CredentialRefs: []CredentialRef{{ID: testDigest, Kind: "oauth"}},
				Health:         health, Restrictions: []Restriction{}, Pricing: pricing, Selectable: true, SelectionReason: "eligible",
			}, {
				RouteKey:               secondRouteKey,
				CatalogRouteProviderID: "anthropic", CatalogRouteModelID: "gpt-5.4", RuntimeModelID: "gpt-5.4", RouteSelector: SelectorForRoute(secondRouteKey, "gpt-5.4"),
				QuotaDomains: []string{"claude-primary"}, Protocols: []string{"openai_chat"},
				CredentialRefs: []CredentialRef{{ID: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Kind: "oauth"}},
				Health:         health, Restrictions: []Restriction{}, Pricing: pricing, Selectable: true, SelectionReason: "eligible fallback",
			}},
		}},
		Aliases: []Alias{{
			Name: "aihub-primary", TierID: "primary", Selectable: true, Reason: "selected",
			Members: []Member{{
				ModelKey: modelKey, MemberRank: 1, ModelScore: "1", SelectionReason: "selected member",
				Candidates: []Candidate{{
					RouteKey: routeKey, CatalogRouteProviderID: "openrouter", CatalogRouteModelID: "openai/gpt-5.4",
					RuntimeModelID: "gpt-5.4", RouteSelector: SelectorForRoute(routeKey, "gpt-5.4"), VariantID: &variantID, RouteRank: 1,
					QuotaDomains: []string{"openai-primary"}, CredentialRefs: []CredentialRef{{ID: testDigest, Kind: "oauth"}},
					Protocols: []string{"openai_chat"}, Health: health, Restrictions: []Restriction{}, Pricing: pricing, SelectionReason: "ranked first",
				}, {
					RouteKey: secondRouteKey, CatalogRouteProviderID: "anthropic", CatalogRouteModelID: "gpt-5.4",
					RuntimeModelID: "gpt-5.4", RouteSelector: SelectorForRoute(secondRouteKey, "gpt-5.4"), VariantID: &variantID, RouteRank: 2,
					QuotaDomains: []string{"claude-primary"}, CredentialRefs: []CredentialRef{{ID: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Kind: "oauth"}},
					Protocols: []string{"openai_chat"}, Health: health, Restrictions: []Restriction{}, Pricing: pricing, SelectionReason: "ranked fallback",
				}},
			}},
		}},
		FailurePolicy: FailurePolicy{
			Mode: "classified_candidate_failover", CredentialAcquisitionTimeoutSeconds: 30,
			AutomaticRetry: false, AutomaticFailover: true, MaxCandidateAttempts: 2,
			FailoverRules: []FailoverRule{{
				RuleID: "transient", HTTPStatuses: []int{429, 503}, ErrorCodes: []string{"overloaded"},
				FailureKinds: []FailureKind{FailureKindCredential, FailureKindTransport},
			}}, ServeStaleOnError: false,
			PreserveFirstError: true, TerminateOwnedRequestOnCancel: true,
		},
	}
	digest, err := ProjectionDigest(&cfg)
	if err != nil {
		panic(err)
	}
	cfg.ProjectionDigest = digest
	return cfg
}

func refreshProjectionDigest(t *testing.T, cfg *Config) {
	t.Helper()
	digest, err := ProjectionDigest(cfg)
	if err != nil {
		t.Fatalf("ProjectionDigest() error = %v", err)
	}
	cfg.ProjectionDigest = digest
}

func TestValidateAcceptsLosslessRouteProjection(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsModelAssignedToTwoTiers(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	other := cfg.Aliases[0]
	other.Name = "aihub-deep"
	other.TierID = "deep"
	cfg.Aliases = append(cfg.Aliases, other)
	refreshProjectionDigest(t, &cfg)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "assigned to tiers") {
		t.Fatalf("Validate() error = %v, want exclusive-tier failure", err)
	}
}

func TestValidateRejectsInvalidClassifiedFailoverPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*FailurePolicy)
		want   string
	}{
		{name: "mode", mutate: func(policy *FailurePolicy) { policy.Mode = "retry" }, want: "mode"},
		{name: "timeout", mutate: func(policy *FailurePolicy) { policy.CredentialAcquisitionTimeoutSeconds = 0 }, want: "credential-acquisition-timeout-seconds"},
		{name: "automatic retry", mutate: func(policy *FailurePolicy) { policy.AutomaticRetry = true }, want: "automatic-retry"},
		{name: "automatic failover", mutate: func(policy *FailurePolicy) { policy.AutomaticFailover = false }, want: "automatic-failover"},
		{name: "candidate attempts", mutate: func(policy *FailurePolicy) { policy.MaxCandidateAttempts = 1 }, want: "max-candidate-attempts"},
		{name: "missing rules", mutate: func(policy *FailurePolicy) { policy.FailoverRules = nil }, want: "failover-rules"},
		{name: "stale result", mutate: func(policy *FailurePolicy) { policy.ServeStaleOnError = true }, want: "serve-stale-on-error"},
		{name: "replace first error", mutate: func(policy *FailurePolicy) { policy.PreserveFirstError = false }, want: "preserve-first-error"},
		{name: "orphan cancellation", mutate: func(policy *FailurePolicy) { policy.TerminateOwnedRequestOnCancel = false }, want: "terminate-owned-request-on-cancel"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			test.mutate(&cfg.FailurePolicy)
			refreshProjectionDigest(t, &cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q failure", err, test.want)
			}
		})
	}
}

func TestValidateRejectsInvalidFailoverRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*FailurePolicy)
		want   string
	}{
		{name: "empty matcher", mutate: func(policy *FailurePolicy) { policy.FailoverRules[0] = FailoverRule{RuleID: "empty"} }, want: "at least one matcher"},
		{name: "unknown kind", mutate: func(policy *FailurePolicy) { policy.FailoverRules[0].FailureKinds = []FailureKind{"unknown"} }, want: "unsupported failure kind"},
		{name: "unsorted statuses", mutate: func(policy *FailurePolicy) { policy.FailoverRules[0].HTTPStatuses = []int{503, 429} }, want: "sorted and unique"},
		{name: "duplicate rule", mutate: func(policy *FailurePolicy) {
			policy.FailoverRules = append(policy.FailoverRules, policy.FailoverRules[0])
		}, want: "duplicate rule"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			test.mutate(&cfg.FailurePolicy)
			refreshProjectionDigest(t, &cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q failure", err, test.want)
			}
		})
	}
}

func TestValidateRejectsVariantSerializedAsModel(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.DirectModels = append(cfg.DirectModels, DirectModel{
		ModelKey: ModelKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4:high"}, Active: true,
	})
	refreshProjectionDigest(t, &cfg)
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a direct model without a route")
	}
}

func TestUnmarshalRejectsUnknownProjectionField(t *testing.T) {
	t.Parallel()
	var cfg Config
	err := yaml.Unmarshal([]byte("schema-version: 3\ngeneration: 1\nsnapshot-digest: "+testDigest+"\nprojection-digest: "+testDigest+"\naliases: []\ndirect-models: []\nfailure-policy:\n  mode: classified_candidate_failover\n  credential-acquisition-timeout-seconds: 30\n  automatic-retry: false\n  automatic-failover: true\n  max-candidate-attempts: 2\n  failover-rules: []\n  serve-stale-on-error: false\n  preserve-first-error: true\n  terminate-owned-request-on-cancel: true\nunknown: true\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Unmarshal() error = %v, want unknown-field failure", err)
	}
}

func TestUnmarshalRejectsRemovedRetryPolicy(t *testing.T) {
	t.Parallel()
	var cfg Config
	err := yaml.Unmarshal([]byte("schema-version: 3\ngeneration: 1\nsnapshot-digest: "+testDigest+"\nprojection-digest: "+testDigest+"\naliases: []\ndirect-models: []\nfailure-policy:\n  mode: classified_candidate_failover\n  credential-acquisition-timeout-seconds: 30\n  automatic-retry: false\n  automatic-failover: true\n  max-candidate-attempts: 2\n  failover-rules: []\n  serve-stale-on-error: false\n  preserve-first-error: true\n  terminate-owned-request-on-cancel: true\nretry-policy: {}\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "retry-policy: unknown field") {
		t.Fatalf("Unmarshal() error = %v, want removed retry-policy rejection", err)
	}
}

func TestUnmarshalRejectsRemovedRequestTimeout(t *testing.T) {
	t.Parallel()
	var cfg Config
	err := yaml.Unmarshal([]byte("schema-version: 3\ngeneration: 1\nsnapshot-digest: "+testDigest+"\nprojection-digest: "+testDigest+"\naliases: []\ndirect-models: []\nfailure-policy:\n  mode: classified_candidate_failover\n  request-timeout-seconds: 30\n  automatic-retry: false\n  automatic-failover: true\n  max-candidate-attempts: 2\n  failover-rules: []\n  serve-stale-on-error: false\n  preserve-first-error: true\n  terminate-owned-request-on-cancel: true\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "request-timeout-seconds: unknown field") {
		t.Fatalf("Unmarshal() error = %v, want removed request timeout rejection", err)
	}
}
