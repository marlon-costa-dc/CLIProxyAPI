package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const (
	contractSnapshotDigest = "sha256:15303dbab83d64d09f79f1f3a22bc09fb3ad5916f2624283f2c6a0ecbe969801"
)

func managementRoutingProjection() *modelrouting.Config {
	modelKey := modelrouting.ModelKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4"}
	routeKey := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: "openai"}
	routeSelector := modelrouting.SelectorForRoute(routeKey, "gpt-5.4")
	credentialID := coreauth.CredentialReferenceID("credential-a")
	pricing := &modelrouting.Pricing{
		Currency: "USD", Unit: "per_million_tokens", SourceID: "models_dev",
		Entries: []modelrouting.PricingEntry{{Name: "input", Amount: "2.5"}, {Name: "output", Amount: "15"}},
	}
	health := modelrouting.Health{Status: "healthy", Selectable: true, ObservedAt: "2026-08-27T18:45:00Z"}
	route := modelrouting.DirectRoute{
		RouteKey: routeKey, CatalogRouteProviderID: "openrouter", CatalogRouteModelID: "openai/gpt-5.4",
		RuntimeModelID: "gpt-5.4", RouteSelector: routeSelector,
		QuotaDomains: []string{"quota-a"}, CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
		Protocols: []string{"openai_chat"}, Restrictions: []modelrouting.Restriction{}, Health: health,
		Pricing: pricing, Selectable: true, SelectionReason: "eligible",
	}
	projection := &modelrouting.Config{
		SchemaVersion: 2, Generation: 1, SnapshotDigest: contractSnapshotDigest,
		ProjectionDigest: contractSnapshotDigest,
		DirectModels: []modelrouting.DirectModel{{
			ModelKey: modelKey, DisplayName: "GPT-5.4", Active: true,
			Variants: []modelrouting.Variant{}, Routes: []modelrouting.DirectRoute{route},
		}},
		Aliases: []modelrouting.Alias{{
			Name: "aihub-primary", TierID: "primary", Selectable: true, Reason: "selected",
			Members: []modelrouting.Member{{
				ModelKey: modelKey, MemberRank: 1, ModelScore: "1", SelectionReason: "selected member",
				Candidates: []modelrouting.Candidate{{
					RouteKey: routeKey, CatalogRouteProviderID: "openrouter",
					CatalogRouteModelID: "openai/gpt-5.4", RuntimeModelID: "gpt-5.4", RouteSelector: routeSelector,
					RouteRank: 1, QuotaDomains: []string{"quota-a"},
					CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
					Protocols:      []string{"openai_chat"}, Health: health, Restrictions: []modelrouting.Restriction{},
					Pricing: pricing, SelectionReason: "ranked first",
				}},
			}},
		}},
		FailurePolicy: modelrouting.FailurePolicy{
			Mode: "classified_candidate_failover", CredentialAcquisitionTimeoutSeconds: 120,
			AutomaticRetry: false, AutomaticFailover: true, MaxCandidateAttempts: 3,
			FailoverRules: []modelrouting.FailoverRule{{
				RuleID: "capacity", HTTPStatuses: []int{429},
				ErrorCodes:   []string{"credential_concurrency_exceeded", "model_cooldown", "rate_limit"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindCredential},
			}, {
				RuleID: "pre-response-transient", HTTPStatuses: []int{408, 500, 502, 503, 504},
				ErrorCodes:   []string{"empty_completion", "empty_stream", "home_unavailable", "upstream_failed"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindEmptyPreResponse, modelrouting.FailureKindTransport, modelrouting.FailureKindUpstreamTimeout},
			}},
			ServeStaleOnError:  false,
			PreserveFirstError: true, TerminateOwnedRequestOnCancel: true,
		},
	}
	digest, err := modelrouting.ProjectionDigest(projection)
	if err != nil {
		panic(err)
	}
	projection.ProjectionDigest = digest
	return projection
}

func TestPutConfigYAMLReturnsActiveDigestReceipt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projection := managementRoutingProjection()
	payload, err := yaml.Marshal(&config.Config{
		Port:               8317,
		CredentialInFlight: config.DefaultCredentialInFlightConfig(),
		ModelRouting:       projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	loadedAt := time.Date(2026, 8, 28, 11, 20, 57, 0, time.UTC)
	published := false
	h.SetConfigPublishHook(func(_ context.Context, body []byte, expected *modelrouting.ActiveIdentityV2, bootstrap bool) (*config.Config, *modelrouting.ActivationReceiptV2, error) {
		if !bootstrap || expected != nil {
			t.Fatalf("publisher precondition = bootstrap %t, expected %+v", bootstrap, expected)
		}
		parsed, errParse := config.ParseConfigBytes(body)
		if errParse != nil {
			t.Fatalf("ParseConfigBytes() error = %v", errParse)
		}
		published = true
		active := modelrouting.ActiveIdentityV2{
			Generation:       projection.Generation,
			SnapshotDigest:   projection.SnapshotDigest,
			ProjectionDigest: projection.ProjectionDigest,
			ConfigDigest:     modelrouting.ConfigDigest(body),
		}
		return parsed, &modelrouting.ActivationReceiptV2{
			Active: active, RoutingSchema: modelrouting.RoutingSchemaInfo{Version: 3, Digest: modelrouting.SchemaDigest()}, LoadedAt: loadedAt,
		}, nil
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(string(payload)))
	c.Request.Header.Set("Content-Type", "application/yaml")
	c.Request.Header.Set("If-None-Match", "*")
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !published {
		t.Fatal("Service-owned CAS publisher was not called")
	}
	var receipt modelrouting.ActivationReceiptV2
	if err = json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Active.Generation != projection.Generation || receipt.Active.SnapshotDigest != contractSnapshotDigest || receipt.Active.ProjectionDigest != projection.ProjectionDigest {
		t.Fatalf("receipt = %+v", receipt)
	}
	wantETag, err := modelrouting.ActiveETag(receipt.Active)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Header().Get("ETag") != wantETag {
		t.Fatalf("ETag = %q, want %q", recorder.Header().Get("ETag"), wantETag)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-write-") {
			t.Fatalf("atomic writer leaked staging file %q", entry.Name())
		}
	}
}

func TestPutConfigYAMLHashesManagementSecretBeforeAtomicPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const plaintext = "management-secret-that-must-not-reach-disk"
	payload := "port: 8317\nremote-management:\n  secret-key: " + plaintext + "\n"
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	h.SetConfigReloadHook(func(_ context.Context, _ *config.Config) error { return nil })
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(payload))
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), plaintext) {
		t.Fatalf("plaintext management secret was published: %s", persisted)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = bcrypt.CompareHashAndPassword([]byte(loaded.RemoteManagement.SecretKey), []byte(plaintext)); err != nil {
		t.Fatalf("persisted secret does not verify as bcrypt: %v", err)
	}
}

func TestPutConfigYAMLRollsBackFileMemoryAndRuntimeOnReloadFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	previousBody := []byte("port: 8317\n")
	if err := os.WriteFile(path, previousBody, 0o600); err != nil {
		t.Fatal(err)
	}
	previousConfig := &config.Config{Port: 8317, CredentialInFlight: config.DefaultCredentialInFlightConfig()}
	h := NewHandler(previousConfig, path, nil)
	errApply := errors.New("runtime rejected new config")
	var appliedPorts []int
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) error {
		appliedPorts = append(appliedPorts, cfg.Port)
		if cfg.Port == 8318 {
			return errApply
		}
		return nil
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader("port: 8318\n"))
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), errApply.Error()) {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(previousBody) {
		t.Fatalf("persisted config = %q, want rollback %q", persisted, previousBody)
	}
	h.mu.Lock()
	activePort := h.cfg.Port
	h.mu.Unlock()
	if activePort != previousConfig.Port {
		t.Fatalf("handler config port = %d, want %d", activePort, previousConfig.Port)
	}
	if len(appliedPorts) != 2 || appliedPorts[0] != 8318 || appliedPorts[1] != 8317 {
		t.Fatalf("runtime applications = %v, want failed new then restored previous", appliedPorts)
	}
}

func TestProjectedInventoryFailsClosedForSuspendedCredential(t *testing.T) {
	projection := managementRoutingProjection()
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "openai", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": "quota-a"},
		UpdatedAt:  time.Date(2026, 8, 27, 18, 45, 0, 0, time.UTC),
	}
	registered := []registry.RegisteredRouteSnapshot{{
		ClientID: "credential-a", RouteChannel: "openai", RuntimeModelID: "gpt-5.4",
		Model: &registry.ModelInfo{ID: "gpt-5.4"}, SuspensionReason: "account suspended",
	}}
	models := projectedInventoryModels(projection, registered, map[string]*coreauth.Auth{"credential-a": auth}, time.Now())
	if len(models) != 1 || len(models[0].Routes) != 1 || len(models[0].Routes[0].Credentials) != 1 {
		t.Fatalf("inventory shape = %+v", models)
	}
	route := models[0].Routes[0]
	credential := route.Credentials[0]
	if route.Selectable || models[0].Active || credential.Health.Selectable || !credential.Suspension.Active {
		t.Fatalf("suspended route failed open: model=%+v route=%+v credential=%+v", models[0], route, credential)
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "credential-a") {
		t.Fatalf("inventory leaked raw credential ID: %s", encoded)
	}
}

func TestBootstrapInventoryFailsLoudlyForIncompleteOrManagedRoutes(t *testing.T) {
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "openai", RouteChannel: "openai", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	model := &registry.ModelInfo{
		ID: "gpt-5.4", CatalogProviderID: "openai", CatalogModelID: "gpt-5.4",
		CatalogRouteProviderID: "openai", CatalogRouteModelID: "gpt-5.4", Protocols: []string{"openai_chat"},
	}
	tests := []struct {
		name  string
		route registry.RegisteredRouteSnapshot
		want  string
	}{
		{
			name:  "missing model facts",
			route: registry.RegisteredRouteSnapshot{ClientID: auth.ID, RouteChannel: "openai", RuntimeModelID: "gpt-5.4"},
			want:  "registered route 0 has no model facts",
		},
		{
			name: "managed alias before projection",
			route: registry.RegisteredRouteSnapshot{
				ClientID: auth.ID, RouteChannel: "openai", RuntimeModelID: "aihub-primary", Model: model,
			},
			want: "registered route 0 contains a managed alias before projection",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := bootstrapInventoryModels(
				[]registry.RegisteredRouteSnapshot{test.route},
				map[string]*coreauth.Auth{auth.ID: auth},
				time.Now(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestBootstrapInventorySurfacesRoutesThatCannotDeclareCatalogFacts pins the
// distinction the bootstrap has to make. Only openai-compatibility models carry
// catalog facts: buildConfigModels (which serves claude-api-key, xai, gemini and
// codex) never sets CatalogProviderID, CatalogModelID, CatalogRouteProviderID,
// CatalogRouteModelID or Protocols. Failing the whole inventory over a route
// whose type has nowhere to put those facts strands generation one, because the
// projection that would supply them can only be published once the inventory
// answers. A route that declares none of them is simply not catalog-declared and
// is surfaced under its channel identity; a route that declares some of them is
// malformed and must still fail loudly.
func TestBootstrapInventorySurfacesRoutesThatCannotDeclareCatalogFacts(t *testing.T) {
	auth := &coreauth.Auth{
		ID: "credential-a", Provider: "anthropic", RouteChannel: "claude", QuotaDomain: "quota-a", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "api_key"},
	}
	auths := map[string]*coreauth.Auth{auth.ID: auth}

	// Exactly what buildConfigModels(entry.Models, "anthropic", "claude")
	// produces for a claude-api-key entry: an identity, and no catalog facts.
	claudeRoute := registry.RegisteredRouteSnapshot{
		ClientID: auth.ID, RouteChannel: "claude", RuntimeModelID: "glm-5.3",
		Model: &registry.ModelInfo{ID: "glm-5.3", DisplayName: "glm-5.3"},
	}
	catalogRoute := registry.RegisteredRouteSnapshot{
		ClientID: auth.ID, RouteChannel: "claude", RuntimeModelID: "gpt-5.4",
		Model: &registry.ModelInfo{
			ID: "gpt-5.4", CatalogProviderID: "openai", CatalogModelID: "gpt-5.4",
			CatalogRouteProviderID: "openai", CatalogRouteModelID: "gpt-5.4",
			Protocols: []string{"openai_chat"},
		},
	}

	t.Run("route without catalog facts surfaces under channel identity", func(t *testing.T) {
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute}, auths, time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 1", len(models))
		}
		if got := models[0].ModelKey.CatalogProviderID; got != "claude" {
			t.Fatalf("catalog provider = %q, want %q", got, "claude")
		}
		if got := models[0].ModelKey.CanonicalModelID; got != "glm-5.3" {
			t.Fatalf("canonical model = %q, want %q", got, "glm-5.3")
		}
	})

	t.Run("catalog-declared route still builds alongside a channel route", func(t *testing.T) {
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute, catalogRoute}, auths, time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 2 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 2", len(models))
		}
	})

	t.Run("partially declared route is malformed and still fails", func(t *testing.T) {
		partial := claudeRoute
		partial.Model = &registry.ModelInfo{ID: "glm-5.3", CatalogProviderID: "zhipuai"}
		_, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{partial}, auths, time.Now(),
		)
		const want = "registered route 0 has invalid catalog model fact"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, want)
		}
	})

	t.Run("managed alias is still rejected even without catalog facts", func(t *testing.T) {
		managed := claudeRoute
		managed.RuntimeModelID = "aihub-primary"
		_, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{managed}, auths, time.Now(),
		)
		const want = "registered route 0 contains a managed alias before projection"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("bootstrapInventoryModels() error = %v, want %q", err, want)
		}
	})

	t.Run("every credential of a surfaced route is kept", func(t *testing.T) {
		secondAuth := &coreauth.Auth{
			ID: "credential-b", Provider: "anthropic", RouteChannel: "claude", QuotaDomain: "quota-b", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "api_key"},
		}
		duplicate := claudeRoute
		duplicate.ClientID = secondAuth.ID
		models, err := bootstrapInventoryModels(
			[]registry.RegisteredRouteSnapshot{claudeRoute, duplicate},
			map[string]*coreauth.Auth{claudeRoute.ClientID: auth, secondAuth.ID: secondAuth},
			time.Now(),
		)
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 || len(models[0].Routes) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models / %d routes, want 1/1", len(models), len(models[0].Routes))
		}
		if got := len(models[0].Routes[0].Credentials); got != 2 {
			t.Fatalf("surfaced route carries %d credentials, want 2 (a duplicate channel+model pair must not drop credentials)", got)
		}
	})

	t.Run("catalog route credentials survive slice growth across three routes of one model", func(t *testing.T) {
		registryRoutes := make([]registry.RegisteredRouteSnapshot, 0, 6)
		registryAuths := map[string]*coreauth.Auth{}
		for channelIndex := 0; channelIndex < 3; channelIndex++ {
			channel := fmt.Sprintf("compat-%d", channelIndex)
			for credentialIndex := 0; credentialIndex < 2; credentialIndex++ {
				credentialID := fmt.Sprintf("credential-%d-%d", channelIndex, credentialIndex)
				registryAuths[credentialID] = &coreauth.Auth{
					ID: credentialID, Provider: channel, RouteChannel: channel,
					QuotaDomain: fmt.Sprintf("quota-%d-%d", channelIndex, credentialIndex), Status: coreauth.StatusActive,
					Attributes: map[string]string{"auth_kind": "api_key"},
				}
				registryRoutes = append(registryRoutes, registry.RegisteredRouteSnapshot{
					ClientID: credentialID, RouteChannel: channel, RuntimeModelID: "glm-5.3",
					Model: &registry.ModelInfo{
						ID: "glm-5.3", CatalogProviderID: "zhipuai", CatalogModelID: "glm-5.3",
						CatalogRouteProviderID: "zhipuai", CatalogRouteModelID: "glm-5.3",
						Protocols: []string{"openai_chat"},
					},
				})
			}
		}
		models, err := bootstrapInventoryModels(registryRoutes, registryAuths, time.Now())
		if err != nil {
			t.Fatalf("bootstrapInventoryModels() error = %v, want nil", err)
		}
		if len(models) != 1 {
			t.Fatalf("bootstrapInventoryModels() built %d models, want 1", len(models))
		}
		for _, route := range models[0].Routes {
			if got := len(route.Credentials); got != 2 {
				t.Fatalf("route %s carries %d credentials, want 2 (slice growth must not orphan route bookkeeping)", route.RouteKey.RouteChannel, got)
			}
		}
		if got := len(models[0].Routes); got != 3 {
			t.Fatalf("model carries %d routes, want 3", got)
		}
	})
}
