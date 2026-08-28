// Package modelrouting owns the versioned routing projection consumed by CLIProxy.
//
// The projection is produced by CCS from an AI Hub snapshot. CLIProxy validates and
// executes it, but never re-ranks candidates or invents catalog facts.
package modelrouting

const (
	ProtocolOpenAIChat        = "openai_chat"
	ProtocolOpenAIResponses   = "openai_responses"
	ProtocolAnthropicMessages = "anthropic_messages"
)

// Config is the native CLIProxy model-routing v1 projection.
type Config struct {
	SchemaVersion    int           `yaml:"schema-version" json:"schema-version"`
	Generation       uint64        `yaml:"generation" json:"generation"`
	SnapshotDigest   string        `yaml:"snapshot-digest" json:"snapshot-digest"`
	ProjectionDigest string        `yaml:"projection-digest" json:"projection-digest"`
	Aliases          []Alias       `yaml:"aliases" json:"aliases"`
	DirectModels     []DirectModel `yaml:"direct-models" json:"direct-models"`
	FailurePolicy    FailurePolicy `yaml:"failure-policy" json:"failure-policy"`
}

// ModelKey identifies one canonical catalog model. A route provider is deliberately
// not part of this identity.
type ModelKey struct {
	CatalogProviderID string `yaml:"catalog-provider-id" json:"catalog-provider-id"`
	CanonicalModelID  string `yaml:"canonical-model-id" json:"canonical-model-id"`
}

// RouteKey identifies one concrete routing channel for a canonical model.
type RouteKey struct {
	CatalogProviderID string `yaml:"catalog-provider-id" json:"catalog-provider-id"`
	CanonicalModelID  string `yaml:"canonical-model-id" json:"canonical-model-id"`
	RouteChannel      string `yaml:"route-channel" json:"route-channel"`
}

// VariantKey identifies a model-owned variant. Variants are never direct models.
type VariantKey struct {
	CatalogProviderID string `yaml:"catalog-provider-id" json:"catalog-provider-id"`
	CanonicalModelID  string `yaml:"canonical-model-id" json:"canonical-model-id"`
	VariantID         string `yaml:"variant-id" json:"variant-id"`
}

// Alias is one AI Hub tier and its already-ranked candidate chain.
type Alias struct {
	Name       string      `yaml:"name" json:"name"`
	TierID     string      `yaml:"tier-id" json:"tier-id"`
	Selectable bool        `yaml:"selectable" json:"selectable"`
	Reason     string      `yaml:"reason" json:"reason"`
	Candidates []Candidate `yaml:"candidates" json:"candidates"`
}

// Candidate references one direct route and an optional nested variant.
type Candidate struct {
	ModelKey               ModelKey        `yaml:"model-key" json:"model-key"`
	RouteChannel           string          `yaml:"route-channel" json:"route-channel"`
	CatalogRouteProviderID string          `yaml:"catalog-route-provider-id" json:"catalog-route-provider-id"`
	CatalogRouteModelID    string          `yaml:"catalog-route-model-id" json:"catalog-route-model-id"`
	RuntimeModelID         string          `yaml:"runtime-model-id" json:"runtime-model-id"`
	RouteSelector          string          `yaml:"route-selector" json:"route-selector"`
	VariantID              *string         `yaml:"variant-id" json:"variant-id"`
	Rank                   int             `yaml:"rank" json:"rank"`
	QuotaDomains           []string        `yaml:"quota-domains" json:"quota-domains"`
	CredentialRefs         []CredentialRef `yaml:"credential-refs" json:"credential-refs"`
	Protocols              []string        `yaml:"protocols" json:"protocols"`
	Health                 Health          `yaml:"health" json:"health"`
	Restrictions           []Restriction   `yaml:"restrictions" json:"restrictions"`
	Pricing                *Pricing        `yaml:"pricing" json:"pricing"`
	SelectionReason        string          `yaml:"selection-reason" json:"selection-reason"`
}

// CredentialRef is an opaque, secret-free reference to a runtime credential.
type CredentialRef struct {
	ID   string `yaml:"id" json:"id"`
	Kind string `yaml:"kind" json:"kind"`
}

// DirectModel describes one published canonical model and all its nested variants
// and real routes.
type DirectModel struct {
	ModelKey    ModelKey      `yaml:"model-key" json:"model-key"`
	DisplayName string        `yaml:"display-name" json:"display-name"`
	Active      bool          `yaml:"active" json:"active"`
	Variants    []Variant     `yaml:"variants" json:"variants"`
	Routes      []DirectRoute `yaml:"routes" json:"routes"`
}

// Variant carries only variant-owned facts and its identity.
type Variant struct {
	VariantKey      VariantKey `yaml:"variant-key" json:"variant-key"`
	DisplayName     *string    `yaml:"display-name" json:"display-name"`
	ReasoningOption *string    `yaml:"reasoning-option" json:"reasoning-option"`
	Protocols       []string   `yaml:"protocols" json:"protocols"`
}

// DirectRoute is a route-aware publication entry. CatalogRouteProviderID is an
// explicit models.dev host identity and is never inferred from RouteChannel.
type DirectRoute struct {
	RouteKey               RouteKey        `yaml:"route-key" json:"route-key"`
	CatalogRouteProviderID string          `yaml:"catalog-route-provider-id" json:"catalog-route-provider-id"`
	CatalogRouteModelID    string          `yaml:"catalog-route-model-id" json:"catalog-route-model-id"`
	RuntimeModelID         string          `yaml:"runtime-model-id" json:"runtime-model-id"`
	RouteSelector          string          `yaml:"route-selector" json:"route-selector"`
	QuotaDomains           []string        `yaml:"quota-domains" json:"quota-domains"`
	CredentialRefs         []CredentialRef `yaml:"credential-refs" json:"credential-refs"`
	Protocols              []string        `yaml:"protocols" json:"protocols"`
	Restrictions           []Restriction   `yaml:"restrictions" json:"restrictions"`
	Health                 Health          `yaml:"health" json:"health"`
	Pricing                *Pricing        `yaml:"pricing" json:"pricing"`
	Selectable             bool            `yaml:"selectable" json:"selectable"`
	SelectionReason        string          `yaml:"selection-reason" json:"selection-reason"`
}

// Restriction explains a config-owned eligibility decision.
type Restriction struct {
	RuleID     string `yaml:"rule-id" json:"rule-id"`
	ConfigPath string `yaml:"config-path" json:"config-path"`
	Active     bool   `yaml:"active" json:"active"`
	Reason     string `yaml:"reason" json:"reason"`
}

// Health is an observed, source-provided health fact.
type Health struct {
	Status     string `yaml:"status" json:"status"`
	Selectable bool   `yaml:"selectable" json:"selectable"`
	ObservedAt string `yaml:"observed-at" json:"observed-at"`
	LatencyMS  *int64 `yaml:"latency-ms" json:"latency-ms"`
}

// Pricing preserves every models.dev price component as a decimal string.
type Pricing struct {
	Currency string         `yaml:"currency" json:"currency"`
	Unit     string         `yaml:"unit" json:"unit"`
	SourceID string         `yaml:"source-id" json:"source-id"`
	Entries  []PricingEntry `yaml:"entries" json:"entries"`
}

// PricingEntry supports both base and conditional/tiered catalog prices without
// converting decimal strings to binary floating point.
type PricingEntry struct {
	Name       string  `yaml:"name" json:"name"`
	Amount     string  `yaml:"amount" json:"amount"`
	TierType   *string `yaml:"tier-type" json:"tier-type"`
	TierSize   *int64  `yaml:"tier-size" json:"tier-size"`
	ContextKey *string `yaml:"context-key" json:"context-key"`
}

// FailurePolicy permits only config-classified movement between the already
// ranked candidate chain. It never permits repeating a candidate attempt.
type FailurePolicy struct {
	Mode                                string         `yaml:"mode" json:"mode"`
	CredentialAcquisitionTimeoutSeconds int            `yaml:"credential-acquisition-timeout-seconds" json:"credential-acquisition-timeout-seconds"`
	AutomaticRetry                      bool           `yaml:"automatic-retry" json:"automatic-retry"`
	AutomaticFailover                   bool           `yaml:"automatic-failover" json:"automatic-failover"`
	MaxCandidateAttempts                int            `yaml:"max-candidate-attempts" json:"max-candidate-attempts"`
	FailoverRules                       []FailoverRule `yaml:"failover-rules" json:"failover-rules"`
	ServeStaleOnError                   bool           `yaml:"serve-stale-on-error" json:"serve-stale-on-error"`
	PreserveFirstError                  bool           `yaml:"preserve-first-error" json:"preserve-first-error"`
	TerminateOwnedRequestOnCancel       bool           `yaml:"terminate-owned-request-on-cancel" json:"terminate-owned-request-on-cancel"`
}

// FailoverRule is one config-owned matcher for advancing to the next ranked
// candidate. Matchers use only typed, secret-free failure facts.
type FailoverRule struct {
	RuleID       string        `yaml:"rule-id" json:"rule-id"`
	HTTPStatuses []int         `yaml:"http-statuses" json:"http-statuses"`
	ErrorCodes   []string      `yaml:"error-codes" json:"error-codes"`
	FailureKinds []FailureKind `yaml:"failure-kinds" json:"failure-kinds"`
}

// FailureKind is a closed technical classification produced by the executor.
type FailureKind string

const (
	FailureKindCredential       FailureKind = "credential"
	FailureKindTransport        FailureKind = "transport"
	FailureKindUpstreamTimeout  FailureKind = "upstream_timeout"
	FailureKindEmptyPreResponse FailureKind = "empty_pre_response"
)
