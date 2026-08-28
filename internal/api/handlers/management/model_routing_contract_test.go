package management

import (
	"context"
	"encoding/json"
	"errors"
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
	contractSnapshotDigest   = "sha256:15303dbab83d64d09f79f1f3a22bc09fb3ad5916f2624283f2c6a0ecbe969801"
	contractProjectionDigest = "sha256:a2d543504bba7caa9a5c925bb1018e484a0331fb0479dbc87e828db51bc275a5"
	contractRouteSelector    = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
)

func managementRoutingProjection() *modelrouting.Config {
	modelKey := modelrouting.ModelKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4"}
	routeKey := modelrouting.RouteKey{CatalogProviderID: "openai", CanonicalModelID: "gpt-5.4", RouteChannel: "openai"}
	credentialID := coreauth.CredentialReferenceID("credential-a")
	pricing := &modelrouting.Pricing{
		Currency: "USD", Unit: "per_million_tokens", SourceID: "models_dev",
		Entries: []modelrouting.PricingEntry{{Name: "input", Amount: "2.5"}, {Name: "output", Amount: "15"}},
	}
	health := modelrouting.Health{Status: "healthy", Selectable: true, ObservedAt: "2026-08-27T18:45:00Z"}
	route := modelrouting.DirectRoute{
		RouteKey: routeKey, CatalogRouteProviderID: "openrouter", CatalogRouteModelID: "openai/gpt-5.4",
		RuntimeModelID: "gpt-5.4", RouteSelector: contractRouteSelector,
		QuotaDomains: []string{"quota-a"}, CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
		Protocols: []string{"openai_chat"}, Restrictions: []modelrouting.Restriction{}, Health: health,
		Pricing: pricing, Selectable: true, SelectionReason: "eligible",
	}
	return &modelrouting.Config{
		SchemaVersion: 1, Generation: 42, SnapshotDigest: contractSnapshotDigest,
		ProjectionDigest: contractProjectionDigest,
		DirectModels: []modelrouting.DirectModel{{
			ModelKey: modelKey, DisplayName: "GPT-5.4", Active: true,
			Variants: []modelrouting.Variant{}, Routes: []modelrouting.DirectRoute{route},
		}},
		Aliases: []modelrouting.Alias{{
			Name: "aihub-primary", TierID: "primary", Selectable: true, Reason: "selected",
			Candidates: []modelrouting.Candidate{{
				ModelKey: modelKey, RouteChannel: "openai", CatalogRouteProviderID: "openrouter",
				CatalogRouteModelID: "openai/gpt-5.4", RuntimeModelID: "gpt-5.4", RouteSelector: contractRouteSelector,
				Rank: 1, QuotaDomains: []string{"quota-a"},
				CredentialRefs: []modelrouting.CredentialRef{{ID: credentialID, Kind: "oauth"}},
				Protocols:      []string{"openai_chat"}, Health: health, Restrictions: []modelrouting.Restriction{},
				Pricing: pricing, SelectionReason: "ranked first",
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
}

func TestPutConfigYAMLReturnsActiveDigestReceipt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelrouting.Activate(nil, time.Time{})
	t.Cleanup(func() { modelrouting.Activate(nil, time.Time{}) })

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := yaml.Marshal(&config.Config{
		Port:               8317,
		CredentialInFlight: config.DefaultCredentialInFlightConfig(),
		ModelRouting:       managementRoutingProjection(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	reloaded := false
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) error {
		reloaded = cfg.ModelRouting != nil
		return nil
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(string(payload)))
	c.Request.Header.Set("Content-Type", "application/yaml")
	h.PutConfigYAML(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !reloaded {
		t.Fatal("runtime reload hook was not called")
	}
	var receipt struct {
		OK               bool   `json:"ok"`
		Generation       uint64 `json:"generation"`
		SnapshotDigest   string `json:"snapshot_digest"`
		ProjectionDigest string `json:"projection_digest"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Generation != 42 || receipt.SnapshotDigest != contractSnapshotDigest || receipt.ProjectionDigest != contractProjectionDigest {
		t.Fatalf("receipt = %+v", receipt)
	}
	active := modelrouting.Active()
	if active == nil || active.ProjectionDigest != receipt.ProjectionDigest {
		t.Fatalf("active = %+v, receipt = %+v", active, receipt)
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
	modelrouting.Activate(nil, time.Time{})
	t.Cleanup(func() { modelrouting.Activate(nil, time.Time{}) })

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
	modelrouting.Activate(nil, time.Time{})
	t.Cleanup(func() { modelrouting.Activate(nil, time.Time{}) })

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
