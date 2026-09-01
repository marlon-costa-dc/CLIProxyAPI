package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type modelRoutingAttemptExecutor struct {
	identifier string

	executeErr error
	countErr   error
	streamErr  error

	executeCalls atomic.Int32
	countCalls   atomic.Int32
	streamCalls  atomic.Int32
	refreshCalls atomic.Int32
	prepareCalls atomic.Int32

	mu         sync.Mutex
	executeIDs []string
	executeFn  func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	prepareFn  func(context.Context, *Auth) (*Auth, error)
	streamFn   func(context.Context) (*cliproxyexecutor.StreamResult, error)
}

func (e *modelRoutingAttemptExecutor) Identifier() string { return e.identifier }

func (e *modelRoutingAttemptExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	e.mu.Lock()
	e.executeIDs = append(e.executeIDs, auth.ID)
	e.mu.Unlock()
	if e.executeFn != nil {
		return e.executeFn(ctx, auth)
	}
	if e.executeErr != nil {
		return cliproxyexecutor.Response{}, e.executeErr
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}

func (e *modelRoutingAttemptExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	if e.streamFn != nil {
		return e.streamFn(ctx)
	}
	if e.streamErr != nil {
		return nil, e.streamErr
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"choices":[{"delta":{"content":"ok"}}]}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *modelRoutingAttemptExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	if e.countErr != nil {
		return cliproxyexecutor.Response{}, e.countErr
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (e *modelRoutingAttemptExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	return auth, nil
}

func (*modelRoutingAttemptExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *modelRoutingAttemptExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	return e.prepareFn != nil
}

func (e *modelRoutingAttemptExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	return e.prepareFn(ctx, auth)
}

func (e *modelRoutingAttemptExecutor) attemptedAuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.executeIDs...)
}

type modelRoutingTestRuntime struct {
	manager          *Manager
	projection       *modelrouting.Config
	requestedModel   string
	firstExecutor    *modelRoutingAttemptExecutor
	secondExecutor   *modelRoutingAttemptExecutor
	firstCredential  string
	secondCredential string
}

func newModelRoutingTestRuntime(t *testing.T, first, second *modelRoutingAttemptExecutor, acquisitionTimeoutSeconds int) modelRoutingTestRuntime {
	t.Helper()
	if first == nil || second == nil {
		t.Fatal("model-routing test executors must not be nil")
	}
	requestedModel := "managed-model-" + uuid.NewString()
	firstCredential := "credential-first-" + uuid.NewString()
	firstAlternateCredential := "credential-first-alternate-" + uuid.NewString()
	secondCredential := "credential-second-" + uuid.NewString()

	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(5, time.Second, 10)
	manager.RegisterExecutor(first)
	manager.RegisterExecutor(second)
	modelKey := modelrouting.ModelKey{CatalogProviderID: "catalog-provider", CanonicalModelID: requestedModel}

	register := func(id, provider string) {
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{
			ID:                     requestedModel,
			CatalogProviderID:      modelKey.CatalogProviderID,
			CatalogModelID:         modelKey.CanonicalModelID,
			CatalogRouteProviderID: provider,
			CatalogRouteModelID:    requestedModel,
			Protocols:              []string{"openai_chat"},
		}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		_, errRegister := manager.Register(context.Background(), &Auth{
			ID: id, Provider: provider, RouteChannel: provider, QuotaDomain: provider + "-quota", Status: StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "quota_domain": provider + "-quota"},
			Metadata:   map[string]any{"disable_cooling": true},
		})
		if errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
		manager.RefreshSchedulerEntry(id)
	}
	register(firstCredential, first.identifier)
	register(firstAlternateCredential, first.identifier)
	register(secondCredential, second.identifier)

	health := modelrouting.Health{Status: "healthy", Selectable: true, ObservedAt: "2026-08-27T18:45:00Z"}
	credentialRefs := func(ids ...string) []modelrouting.CredentialRef {
		refs := make([]modelrouting.CredentialRef, 0, len(ids))
		for _, id := range ids {
			refs = append(refs, modelrouting.CredentialRef{ID: CredentialReferenceID(id), Kind: "oauth"})
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		return refs
	}
	firstRefs := credentialRefs(firstCredential, firstAlternateCredential)
	secondRefs := credentialRefs(secondCredential)
	route := func(provider string, refs []modelrouting.CredentialRef) modelrouting.DirectRoute {
		key := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: provider}
		return modelrouting.DirectRoute{
			RouteKey: key, CatalogRouteProviderID: provider, CatalogRouteModelID: requestedModel,
			RuntimeModelID: requestedModel, RouteSelector: modelrouting.SelectorForRoute(key, requestedModel),
			QuotaDomains: []string{provider + "-quota"}, CredentialRefs: refs,
			Protocols: []string{"openai_chat"}, Restrictions: []modelrouting.Restriction{},
			Health: health, Selectable: true, SelectionReason: "eligible",
		}
	}
	firstRoute := route(first.identifier, firstRefs)
	secondRoute := route(second.identifier, secondRefs)
	candidate := func(rank int, route modelrouting.DirectRoute) modelrouting.Candidate {
		return modelrouting.Candidate{
			RouteKey:               route.RouteKey,
			CatalogRouteProviderID: route.CatalogRouteProviderID, CatalogRouteModelID: route.CatalogRouteModelID,
			RuntimeModelID: route.RuntimeModelID, RouteSelector: route.RouteSelector, RouteRank: rank,
			QuotaDomains:   append([]string(nil), route.QuotaDomains...),
			CredentialRefs: append([]modelrouting.CredentialRef(nil), route.CredentialRefs...),
			Protocols:      append([]string(nil), route.Protocols...), Health: health,
			Restrictions: []modelrouting.Restriction{}, SelectionReason: "ranked",
		}
	}
	projection := &modelrouting.Config{
		SchemaVersion: 2, Generation: 1,
		SnapshotDigest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProjectionDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		DirectModels: []modelrouting.DirectModel{{
			ModelKey: modelKey, DisplayName: requestedModel, Active: true,
			Variants: []modelrouting.Variant{}, Routes: []modelrouting.DirectRoute{firstRoute, secondRoute},
		}},
		Aliases: []modelrouting.Alias{{
			Name: "aihub-primary", TierID: "primary", Selectable: true, Reason: "selected",
			Members: []modelrouting.Member{{
				ModelKey: modelKey, MemberRank: 1, ModelScore: "1", SelectionReason: "selected member",
				Candidates: []modelrouting.Candidate{candidate(1, firstRoute), candidate(2, secondRoute)},
			}},
		}},
		FailurePolicy: modelrouting.FailurePolicy{
			Mode: "classified_candidate_failover", CredentialAcquisitionTimeoutSeconds: acquisitionTimeoutSeconds,
			AutomaticRetry: false, AutomaticFailover: true, MaxCandidateAttempts: 2,
			FailoverRules: []modelrouting.FailoverRule{{
				RuleID: "capacity", HTTPStatuses: []int{429},
				ErrorCodes:   []string{"credential_concurrency_exceeded", "model_cooldown", "rate_limit"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindCredential},
			}, {
				RuleID: "pre-response-transient", HTTPStatuses: []int{408, 500, 502, 503, 504},
				ErrorCodes:   []string{"empty_completion", "empty_stream", "home_unavailable", "upstream_failed"},
				FailureKinds: []modelrouting.FailureKind{modelrouting.FailureKindEmptyPreResponse, modelrouting.FailureKindTransport, modelrouting.FailureKindUpstreamTimeout},
			}}, ServeStaleOnError: false,
			PreserveFirstError: true, TerminateOwnedRequestOnCancel: true,
		},
	}
	digest, errDigest := modelrouting.ProjectionDigest(projection)
	if errDigest != nil {
		t.Fatalf("ProjectionDigest() error = %v", errDigest)
	}
	projection.ProjectionDigest = digest
	if errValidate := projection.Validate(); errValidate != nil {
		t.Fatalf("projection.Validate() error = %v", errValidate)
	}
	prepared, errPrepare := manager.PrepareModelRouting(projection)
	if errPrepare != nil {
		t.Fatalf("PrepareModelRouting() error = %v", errPrepare)
	}
	if errActivate := manager.ActivatePreparedModelRouting(prepared); errActivate != nil {
		t.Fatalf("ActivatePreparedModelRouting() error = %v", errActivate)
	}
	return modelRoutingTestRuntime{
		manager: manager, projection: projection, requestedModel: "aihub-primary", firstExecutor: first, secondExecutor: second,
		firstCredential: firstCredential, secondCredential: secondCredential,
	}
}

func TestPrepareModelRoutingRejectsInvalidProjectionWithoutReplacingActive(t *testing.T) {
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	before, matchedBefore, errBefore := runtime.manager.modelRoutingCandidates(runtime.requestedModel, modelRoutingOptions())
	if errBefore != nil || !matchedBefore || len(before) != 2 {
		t.Fatalf("active candidates before invalid update = %#v matched=%t error=%v", before, matchedBefore, errBefore)
	}

	invalid := *runtime.projection
	invalid.FailurePolicy.AutomaticFailover = false
	digest, errDigest := modelrouting.ProjectionDigest(&invalid)
	if errDigest != nil {
		t.Fatal(errDigest)
	}
	invalid.ProjectionDigest = digest
	if _, errRouting := runtime.manager.PrepareModelRouting(&invalid); errRouting == nil {
		t.Fatal("PrepareModelRouting() accepted disabled classified failover")
	}
	after, matchedAfter, errAfter := runtime.manager.modelRoutingCandidates(runtime.requestedModel, modelRoutingOptions())
	if errAfter != nil || !matchedAfter || len(after) != 2 {
		t.Fatalf("active candidates after invalid update = %#v matched=%t error=%v", after, matchedAfter, errAfter)
	}
	if after[0].RouteSelector != before[0].RouteSelector || after[1].RouteSelector != before[1].RouteSelector {
		t.Fatal("invalid projection replaced the active routing table")
	}
}

func modelRoutingOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: make(http.Header)}
}

func TestModelRoutingBootstrapSelectorExecutesRegistryRouteBeforeProjection(t *testing.T) {
	routeChannel := "bootstrap-route-" + uuid.NewString()
	credentialID := "bootstrap-credential-" + uuid.NewString()
	runtimeModelID := "bootstrap-model-" + uuid.NewString()
	modelKey := modelrouting.ModelKey{CatalogProviderID: "catalog-provider", CanonicalModelID: runtimeModelID}
	routeKey := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: routeChannel}
	selector := modelrouting.SelectorForRoute(routeKey, runtimeModelID)

	executor := &modelRoutingAttemptExecutor{identifier: routeChannel}
	manager := NewManager(nil, nil, NoopHook{})
	manager.RegisterExecutor(executor)
	_, errRegister := manager.Register(context.Background(), &Auth{
		ID: credentialID, Provider: routeChannel, RouteChannel: routeChannel, QuotaDomain: routeChannel + "-quota", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credentialID, routeChannel, []*registry.ModelInfo{{
		ID: runtimeModelID, CatalogProviderID: modelKey.CatalogProviderID, CatalogModelID: modelKey.CanonicalModelID,
		CatalogRouteProviderID: routeChannel, CatalogRouteModelID: runtimeModelID, Protocols: []string{"openai_chat"},
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credentialID) })

	opts := modelRoutingOptions()
	opts.Headers.Set(probeRouteSelectorHeader, selector)
	response, errExecute := manager.Execute(
		context.Background(), []string{routeChannel},
		cliproxyexecutor.Request{Model: runtimeModelID, Payload: []byte(`{"messages":[]}`)}, opts,
	)
	if errExecute != nil {
		t.Fatalf("Execute() bootstrap selector error = %v", errExecute)
	}
	if executor.executeCalls.Load() != 1 {
		t.Fatalf("bootstrap route executions = %d, want 1", executor.executeCalls.Load())
	}
	if got := response.DownstreamHeaders.Get(probeRouteSelectorHeader); got != selector {
		t.Fatalf("bootstrap selector evidence = %q, want %q", got, selector)
	}
}

func TestModelRoutingBootstrapSelectorFailsLoudlyForIncompleteRouteFacts(t *testing.T) {
	routeChannel := "bootstrap-route-" + uuid.NewString()
	credentialID := "bootstrap-credential-" + uuid.NewString()
	runtimeModelID := "bootstrap-model-" + uuid.NewString()
	modelKey := modelrouting.ModelKey{CatalogProviderID: "catalog-provider", CanonicalModelID: runtimeModelID}
	routeKey := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: routeChannel}
	selector := modelrouting.SelectorForRoute(routeKey, runtimeModelID)

	executor := &modelRoutingAttemptExecutor{identifier: routeChannel}
	manager := NewManager(nil, nil, NoopHook{})
	manager.RegisterExecutor(executor)
	_, errRegister := manager.Register(context.Background(), &Auth{
		ID: credentialID, Provider: routeChannel, RouteChannel: routeChannel, QuotaDomain: routeChannel + "-quota", Status: StatusActive,
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credentialID, routeChannel, []*registry.ModelInfo{{
		ID: runtimeModelID, CatalogProviderID: modelKey.CatalogProviderID, CatalogModelID: modelKey.CanonicalModelID,
		CatalogRouteProviderID: routeChannel, CatalogRouteModelID: runtimeModelID,
	}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credentialID) })

	opts := modelRoutingOptions()
	opts.Headers.Set(probeRouteSelectorHeader, selector)
	_, errExecute := manager.Execute(
		context.Background(), []string{routeChannel},
		cliproxyexecutor.Request{Model: runtimeModelID, Payload: []byte(`{"messages":[]}`)}, opts,
	)
	if errExecute == nil || !strings.Contains(errExecute.Error(), "registered route 0 has no explicit protocols") {
		t.Fatalf("Execute() error = %v, want incomplete-route failure", errExecute)
	}
	if executor.executeCalls.Load() != 0 {
		t.Fatalf("bootstrap route executions = %d, want 0", executor.executeCalls.Load())
	}
}

func TestModelRoutingVariantUsesCanonicalProtocolAppliers(t *testing.T) {
	variantID := "high"
	option := "high"
	candidate := RoutingCandidate{
		Channel: "route", RuntimeModelID: "runtime-model", RouteSelector: modelrouting.SelectorForRoute(
			modelrouting.RouteKey{ModelKey: modelrouting.ModelKey{CatalogProviderID: "catalog", CanonicalModelID: "model"}, RouteChannel: "route"},
			"runtime-model",
		),
		VariantID: &variantID, VariantReasoningOption: &option,
	}
	tests := []struct {
		name       string
		format     sdktranslator.Format
		protocol   string
		assertions map[string]string
	}{
		{name: "OpenAI chat", format: sdktranslator.FormatOpenAI, protocol: "openai_chat", assertions: map[string]string{"reasoning_effort": "high"}},
		{name: "OpenAI responses", format: sdktranslator.FormatOpenAIResponse, protocol: "openai_responses", assertions: map[string]string{"reasoning.effort": "high"}},
		{name: "Anthropic messages", format: sdktranslator.FormatClaude, protocol: "anthropic_messages", assertions: map[string]string{"thinking.type": "adaptive", "output_config.effort": "high"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attemptCandidate := candidate
			attemptCandidate.Protocols = []string{test.protocol}
			prepared, err := prepareRoutingCandidateRequest(
				cliproxyexecutor.Request{Model: "aihub-primary", Payload: []byte(`{"messages":[]}`)},
				cliproxyexecutor.Options{SourceFormat: test.format},
				attemptCandidate,
			)
			if err != nil {
				t.Fatalf("prepareRoutingCandidateRequest() error = %v", err)
			}
			if prepared.Model != candidate.RuntimeModelID {
				t.Fatalf("prepared model = %q, want %q", prepared.Model, candidate.RuntimeModelID)
			}
			for path, want := range test.assertions {
				if got := gjson.GetBytes(prepared.Payload, path).String(); got != want {
					t.Fatalf("%s = %q, want %q; payload=%s", path, got, want, prepared.Payload)
				}
			}
		})
	}
}

func TestPrepareModelRoutingRejectsUnknownVariantExecutorWithoutReplacingActive(t *testing.T) {
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	before, _, errBefore := runtime.manager.modelRoutingCandidates(runtime.requestedModel, modelRoutingOptions())
	if errBefore != nil {
		t.Fatal(errBefore)
	}

	invalid := *runtime.projection
	invalid.DirectModels = append([]modelrouting.DirectModel(nil), runtime.projection.DirectModels...)
	invalid.DirectModels[0].Variants = []modelrouting.Variant{{
		VariantKey: modelrouting.VariantKey{
			ModelKey:  invalid.DirectModels[0].ModelKey,
			VariantID: "invalid",
		},
		ReasoningOption: stringPointer("not-an-executor-option"),
		Protocols:       []string{"openai_chat"},
	}}
	digest, errDigest := modelrouting.ProjectionDigest(&invalid)
	if errDigest != nil {
		t.Fatal(errDigest)
	}
	invalid.ProjectionDigest = digest
	if _, err := runtime.manager.PrepareModelRouting(&invalid); err == nil || !strings.Contains(err.Error(), "not-an-executor-option") {
		t.Fatalf("PrepareModelRouting() error = %v, want variant executor rejection", err)
	}
	after, _, errAfter := runtime.manager.modelRoutingCandidates(runtime.requestedModel, modelRoutingOptions())
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if len(after) != len(before) || after[0].VariantID != nil {
		t.Fatalf("invalid variant projection changed active candidates: before=%+v after=%+v", before, after)
	}
}

func stringPointer(value string) *string { return &value }

func TestModelRoutingFailureMatrixAdvancesOnlyForConfiguredClasses(t *testing.T) {
	statuses := []int{
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadRequest,
		http.StatusRequestTimeout,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
	for _, status := range statuses {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			firstCause := &Error{Code: "upstream_failure", Message: "first upstream cause", HTTPStatus: status}
			first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: firstCause}
			second := &modelRoutingAttemptExecutor{identifier: "route-second"}
			runtime := newModelRoutingTestRuntime(t, first, second, 2)

			_, errExecute := runtime.manager.Execute(
				context.Background(),
				[]string{first.identifier, second.identifier},
				cliproxyexecutor.Request{Model: runtime.requestedModel, Payload: []byte(`{"messages":[]}`)},
				modelRoutingOptions(),
			)
			classified := status == http.StatusTooManyRequests || status == http.StatusRequestTimeout ||
				status == http.StatusInternalServerError || status == http.StatusBadGateway ||
				status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
			if classified {
				if errExecute != nil {
					t.Fatalf("Execute() error = %v, want classified failover success", errExecute)
				}
			} else if !errors.Is(errExecute, firstCause) {
				t.Fatalf("Execute() error = %v, want exact first cause %v", errExecute, firstCause)
			}
			if got := first.executeCalls.Load(); got != 1 {
				t.Fatalf("first route calls = %d, want 1", got)
			}
			if got := len(first.attemptedAuthIDs()); got != 1 {
				t.Fatalf("first route credential attempts = %d, want 1", got)
			}
			wantSecond := int32(0)
			if classified {
				wantSecond = 1
			}
			if got := second.executeCalls.Load(); got != wantSecond {
				t.Fatalf("second route calls = %d, want %d", got, wantSecond)
			}
			if got := first.refreshCalls.Load(); got != 0 {
				t.Fatalf("credential refresh calls = %d, want 0", got)
			}
		})
	}
}

func TestModelRoutingExhaustionPreservesFirstCauseAndJoinsLaterCause(t *testing.T) {
	firstCause := &Error{Code: "upstream_failed", Message: "first upstream cause", HTTPStatus: http.StatusServiceUnavailable}
	secondCause := &Error{Code: "upstream_failed", Message: "second upstream cause", HTTPStatus: http.StatusInternalServerError}
	first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: firstCause}
	second := &modelRoutingAttemptExecutor{identifier: "route-second", executeErr: secondCause}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)

	_, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if !errors.Is(errExecute, firstCause) || !errors.Is(errExecute, secondCause) {
		t.Fatalf("Execute() error = %v, want first and later causes", errExecute)
	}
	if status := statusCodeFromError(errExecute); status != firstCause.HTTPStatus {
		t.Fatalf("status = %d, want first-cause status %d", status, firstCause.HTTPStatus)
	}
	if first.executeCalls.Load() != 1 || second.executeCalls.Load() != 1 {
		t.Fatalf("candidate attempts = %d/%d, want exactly 1/1", first.executeCalls.Load(), second.executeCalls.Load())
	}
	var carrier interface{ SafeResponseHeaders() http.Header }
	if !errors.As(errExecute, &carrier) || carrier == nil {
		t.Fatalf("execution error has no evidence headers: %v", errExecute)
	}
	headers := carrier.SafeResponseHeaders()
	if got := headers.Get("X-CLIProxy-Attempts"); got != "2" {
		t.Fatalf("X-CLIProxy-Attempts = %q, want 2", got)
	}
	var evidence []modelRoutingAttemptEvidence
	if err := json.Unmarshal([]byte(headers.Get("X-CLIProxy-Failover-Evidence")), &evidence); err != nil {
		t.Fatalf("decode failover evidence: %v", err)
	}
	if len(evidence) != 2 || evidence[0].RuleID != "pre-response-transient" || evidence[1].RuleID != "pre-response-transient" {
		t.Fatalf("failover evidence = %+v", evidence)
	}
	if strings.Contains(headers.Get("X-CLIProxy-Failover-Evidence"), runtime.firstCredential) ||
		strings.Contains(headers.Get("X-CLIProxy-Failover-Evidence"), runtime.secondCredential) {
		t.Fatalf("failover evidence leaked a raw credential: %s", headers.Get("X-CLIProxy-Failover-Evidence"))
	}
}

func TestModelRoutingConfiguredErrorCodeAndTransportKindCanAdvance(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "error code", err: &Error{Code: "upstream_failed", Message: "classified by code", HTTPStatus: http.StatusTeapot}},
		{name: "transport kind", err: io.ErrUnexpectedEOF},
		{
			name: "connection refused transport kind",
			err: &url.Error{
				Op:  "Post",
				URL: "http://127.0.0.1:55057/v1/chat/completions",
				Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: test.err}
			second := &modelRoutingAttemptExecutor{identifier: "route-second"}
			runtime := newModelRoutingTestRuntime(t, first, second, 2)
			response, errExecute := runtime.manager.Execute(
				context.Background(), []string{first.identifier, second.identifier},
				cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions(),
			)
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if first.executeCalls.Load() != 1 || second.executeCalls.Load() != 1 {
				t.Fatalf("candidate attempts = %d/%d, want 1/1", first.executeCalls.Load(), second.executeCalls.Load())
			}
			if got := response.DownstreamHeaders.Get("X-CLIProxy-Attempts"); got != "2" {
				t.Fatalf("X-CLIProxy-Attempts = %q, want 2", got)
			}
		})
	}
}

func TestModelRoutingRetryableFlagAloneCannotAdvance(t *testing.T) {
	firstCause := &Error{Code: "unconfigured", Message: "legacy retryable flag", Retryable: true, HTTPStatus: http.StatusTeapot}
	first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: firstCause}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)

	_, errExecute := runtime.manager.Execute(
		context.Background(), []string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions(),
	)
	if !errors.Is(errExecute, firstCause) {
		t.Fatalf("Execute() error = %v, want unconfigured first cause", errExecute)
	}
	if first.executeCalls.Load() != 1 || second.executeCalls.Load() != 0 {
		t.Fatalf("candidate attempts = %d/%d, want 1/0", first.executeCalls.Load(), second.executeCalls.Load())
	}
}

func TestModelRoutingExecutionJoinsFirstCauseWithResultPersistenceFailure(t *testing.T) {
	firstCause := &Error{Code: "upstream_failure", Message: "first upstream cause", HTTPStatus: http.StatusServiceUnavailable}
	persistCause := errors.New("injected result persistence failure")
	first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: firstCause}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	runtime.manager.store = &failingAuthStore{saveErr: persistCause}

	_, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if !errors.Is(errExecute, firstCause) {
		t.Fatalf("Execute() error = %v, want first upstream cause", errExecute)
	}
	if !errors.Is(errExecute, persistCause) {
		t.Fatalf("Execute() error = %v, want result persistence cause", errExecute)
	}
	if first.executeCalls.Load() != 1 || second.executeCalls.Load() != 0 {
		t.Fatalf("execution attempts after joined failure: first=%d second=%d", first.executeCalls.Load(), second.executeCalls.Load())
	}
}

func TestModelRoutingSuccessfulUpstreamFailsWhenResultPersistenceFails(t *testing.T) {
	persistCause := errors.New("injected result persistence failure")
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	runtime.manager.store = &failingAuthStore{saveErr: persistCause}

	_, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if !errors.Is(errExecute, persistCause) {
		t.Fatalf("Execute() error = %v, want result persistence cause", errExecute)
	}
	if first.executeCalls.Load() != 1 || second.executeCalls.Load() != 0 {
		t.Fatalf("execution attempts after persistence failure: first=%d second=%d", first.executeCalls.Load(), second.executeCalls.Load())
	}
}

func TestModelRoutingCredentialPreparationPersistenceFailureStopsBeforeUpstream(t *testing.T) {
	persistCause := errors.New("injected credential persistence failure")
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	first.prepareFn = func(_ context.Context, auth *Auth) (*Auth, error) {
		prepared := auth.Clone()
		prepared.Metadata["prepared"] = true
		return prepared, nil
	}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	runtime.manager.store = &failingAuthStore{saveErr: persistCause}

	_, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if !errors.Is(errExecute, persistCause) {
		t.Fatalf("Execute() error = %v, want credential persistence cause", errExecute)
	}
	if first.prepareCalls.Load() != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1", first.prepareCalls.Load())
	}
	if first.executeCalls.Load() != 0 || second.executeCalls.Load() != 0 {
		t.Fatalf("upstream ran after credential persistence failure: first=%d second=%d", first.executeCalls.Load(), second.executeCalls.Load())
	}
}

func TestModelRoutingAllExecutionSurfacesAttemptEachCandidateOnce(t *testing.T) {
	firstCause := &Error{Code: "upstream_failure", Message: "first upstream cause", HTTPStatus: http.StatusInternalServerError}
	tests := []struct {
		name  string
		run   func(modelRoutingTestRuntime) error
		calls func(*modelRoutingAttemptExecutor) int32
	}{
		{
			name: "execute",
			run: func(runtime modelRoutingTestRuntime) error {
				_, err := runtime.manager.Execute(context.Background(), []string{"route-first", "route-second"}, cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions())
				return err
			},
			calls: func(executor *modelRoutingAttemptExecutor) int32 { return executor.executeCalls.Load() },
		},
		{
			name: "count tokens",
			run: func(runtime modelRoutingTestRuntime) error {
				_, err := runtime.manager.ExecuteCount(context.Background(), []string{"route-first", "route-second"}, cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions())
				return err
			},
			calls: func(executor *modelRoutingAttemptExecutor) int32 { return executor.countCalls.Load() },
		},
		{
			name: "stream",
			run: func(runtime modelRoutingTestRuntime) error {
				_, err := runtime.manager.ExecuteStream(context.Background(), []string{"route-first", "route-second"}, cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions())
				return err
			},
			calls: func(executor *modelRoutingAttemptExecutor) int32 { return executor.streamCalls.Load() },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := &modelRoutingAttemptExecutor{identifier: "route-first", executeErr: firstCause, countErr: firstCause, streamErr: firstCause}
			second := &modelRoutingAttemptExecutor{identifier: "route-second"}
			runtime := newModelRoutingTestRuntime(t, first, second, 2)
			errExecute := test.run(runtime)
			if errExecute != nil {
				t.Fatalf("execution error = %v, want classified failover success", errExecute)
			}
			if got := test.calls(first); got != 1 {
				t.Fatalf("first route calls = %d, want 1", got)
			}
			if got := test.calls(second); got != 1 {
				t.Fatalf("second route calls = %d, want 1", got)
			}
		})
	}
}

func TestModelRoutingCredentialAcquisitionTimeoutCanAdvanceByConfiguredKind(t *testing.T) {
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	first.prepareFn = func(ctx context.Context, auth *Auth) (*Auth, error) {
		<-ctx.Done()
		return auth, ctx.Err()
	}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 1)

	response, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want classified acquisition-timeout failover", errExecute)
	}
	if got := first.prepareCalls.Load(); got != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1", got)
	}
	if first.executeCalls.Load() != 0 || second.executeCalls.Load() != 1 {
		t.Fatalf("upstream attempts after acquisition timeout: first=%d second=%d", first.executeCalls.Load(), second.executeCalls.Load())
	}
	if got := response.DownstreamHeaders.Get("X-CLIProxy-Attempts"); got != "2" {
		t.Fatalf("X-CLIProxy-Attempts = %q, want 2", got)
	}
}

func TestModelRoutingAcquisitionDeadlineDoesNotReachConnectedExecution(t *testing.T) {
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	first.prepareFn = func(ctx context.Context, auth *Auth) (*Auth, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return auth, errors.New("credential acquisition context has no deadline")
		}
		return auth, nil
	}
	deadlineObserved := make(chan bool, 1)
	first.executeFn = func(ctx context.Context, _ *Auth) (cliproxyexecutor.Response, error) {
		_, hasDeadline := ctx.Deadline()
		deadlineObserved <- hasDeadline
		return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
	}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 1)

	_, errExecute := runtime.manager.Execute(
		context.Background(),
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if hasDeadline := <-deadlineObserved; hasDeadline {
		t.Fatal("credential acquisition deadline leaked into connected execution")
	}
}

func TestModelRoutingCallerCancellationTerminatesOwnedStream(t *testing.T) {
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	streamContextCanceled := make(chan error, 1)
	allowSourceExit := make(chan struct{})
	first.streamFn = func(ctx context.Context) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"choices":[{"delta":{"content":"connected"}}]}`)}
		go func() {
			<-ctx.Done()
			streamContextCanceled <- ctx.Err()
			<-allowSourceExit
			close(chunks)
		}()
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)
	ctx, cancel := context.WithCancel(context.Background())

	stream, errStream := runtime.manager.ExecuteStream(
		ctx,
		[]string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel},
		modelRoutingOptions(),
	)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	firstChunk, open := <-stream.Chunks
	if !open || firstChunk.Err != nil {
		t.Fatalf("first connected chunk = %#v open=%t", firstChunk, open)
	}
	cancel()
	chunk, open := <-stream.Chunks
	if !open || !errors.Is(chunk.Err, context.Canceled) {
		t.Fatalf("cancellation chunk = %#v open=%t, want context.Canceled", chunk, open)
	}
	if errSource := <-streamContextCanceled; !errors.Is(errSource, context.Canceled) {
		t.Fatalf("source context error = %v, want context.Canceled", errSource)
	}
	close(allowSourceExit)
	for range stream.Chunks {
	}
	if first.streamCalls.Load() != 1 || second.streamCalls.Load() != 0 {
		t.Fatalf("stream attempts after cancellation: first=%d second=%d", first.streamCalls.Load(), second.streamCalls.Load())
	}
}

func TestModelRoutingNeverFailsOverAfterFirstStreamPayload(t *testing.T) {
	firstCause := &Error{Code: "upstream_failed", Message: "failed after payload", HTTPStatus: http.StatusServiceUnavailable}
	first := &modelRoutingAttemptExecutor{identifier: "route-first"}
	first.streamFn = func(context.Context) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 2)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"choices":[{"delta":{"content":"committed"}}]}`)}
		chunks <- cliproxyexecutor.StreamChunk{Err: firstCause}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	second := &modelRoutingAttemptExecutor{identifier: "route-second"}
	runtime := newModelRoutingTestRuntime(t, first, second, 2)

	stream, errStream := runtime.manager.ExecuteStream(
		context.Background(), []string{first.identifier, second.identifier},
		cliproxyexecutor.Request{Model: runtime.requestedModel}, modelRoutingOptions(),
	)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var terminal error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			terminal = chunk.Err
		}
	}
	if !errors.Is(terminal, firstCause) {
		t.Fatalf("terminal stream error = %v, want original post-payload cause", terminal)
	}
	if first.streamCalls.Load() != 1 || second.streamCalls.Load() != 0 {
		t.Fatalf("stream attempts = %d/%d, want 1/0", first.streamCalls.Load(), second.streamCalls.Load())
	}
}
