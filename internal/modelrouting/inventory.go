package modelrouting

import "time"

// ActiveProjection is the digest receipt for the currently active projection.
type ActiveProjection struct {
	Generation       uint64    `json:"generation"`
	SnapshotDigest   string    `json:"snapshot_digest"`
	ProjectionDigest string    `json:"projection_digest"`
	LoadedAt         time.Time `json:"loaded_at"`
}

// BinaryProvenance identifies the exact CLIProxy binary serving an inventory.
type BinaryProvenance struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

// Inventory is the route-aware, secret-free management response.
type Inventory struct {
	SchemaVersion    int               `json:"schema_version"`
	GeneratedAt      time.Time         `json:"generated_at"`
	Active           *ActiveProjection `json:"active"`
	BinaryProvenance BinaryProvenance  `json:"binary_provenance"`
	Models           []InventoryModel  `json:"models"`
}

type InventoryModel struct {
	ModelKey    ModelKeyJSON       `json:"model_key"`
	DisplayName string             `json:"display_name"`
	Active      bool               `json:"active"`
	Variants    []InventoryVariant `json:"variants"`
	Routes      []InventoryRoute   `json:"routes"`
}

type ModelKeyJSON struct {
	CatalogProviderID string `json:"catalog_provider_id"`
	CanonicalModelID  string `json:"canonical_model_id"`
}

type RouteKeyJSON struct {
	CatalogProviderID string `json:"catalog_provider_id"`
	CanonicalModelID  string `json:"canonical_model_id"`
	RouteChannel      string `json:"route_channel"`
}

type VariantKeyJSON struct {
	CatalogProviderID string `json:"catalog_provider_id"`
	CanonicalModelID  string `json:"canonical_model_id"`
	VariantID         string `json:"variant_id"`
}

type InventoryVariant struct {
	VariantKey  VariantKeyJSON `json:"variant_key"`
	DisplayName *string        `json:"display_name"`
	Protocols   []string       `json:"protocols"`
}

type InventoryRoute struct {
	RouteKey               RouteKeyJSON           `json:"route_key"`
	CatalogRouteProviderID string                 `json:"catalog_route_provider_id"`
	CatalogRouteModelID    string                 `json:"catalog_route_model_id"`
	RuntimeModelID         string                 `json:"runtime_model_id"`
	RouteSelector          string                 `json:"route_selector"`
	QuotaDomains           []string               `json:"quota_domains"`
	Protocols              []string               `json:"protocols"`
	Restrictions           []InventoryRestriction `json:"restrictions"`
	Health                 InventoryHealth        `json:"health"`
	Selectable             bool                   `json:"selectable"`
	SelectionReason        string                 `json:"selection_reason"`
	Credentials            []InventoryCredential  `json:"credentials"`
}

type InventoryRestriction struct {
	RuleID     string `json:"rule_id"`
	ConfigPath string `json:"config_path"`
	Active     bool   `json:"active"`
	Reason     string `json:"reason"`
}

type InventoryHealth struct {
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
	ObservedAt string `json:"observed_at"`
	LatencyMS  *int64 `json:"latency_ms"`
}

type InventoryCredentialRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type InventoryCredential struct {
	CredentialRef InventoryCredentialRef `json:"credential_ref"`
	QuotaDomain   string                 `json:"quota_domain"`
	Health        InventoryHealth        `json:"health"`
	Quota         InventoryQuota         `json:"quota"`
	Suspension    InventorySuspension    `json:"suspension"`
	Restrictions  []InventoryRestriction `json:"restrictions"`
}

type InventoryQuota struct {
	Status    string  `json:"status"`
	Remaining *string `json:"remaining"`
	ResetsAt  *string `json:"resets_at"`
	Reason    *string `json:"reason"`
}

type InventorySuspension struct {
	Active    bool    `json:"active"`
	Reason    *string `json:"reason"`
	ResumesAt *string `json:"resumes_at"`
}
