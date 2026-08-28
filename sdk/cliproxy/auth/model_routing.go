package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	thinkingclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	thinkingcodex "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	thinkingopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	probeRouteSelectorHeader   = "X-CLIProxy-Route-Selector"
	probeVariantIDHeader       = "X-CLIProxy-Variant-ID"
	effectiveModelHeader       = "X-CLIProxy-Effective-Model"
	quotaDomainHeader          = "X-CLIProxy-Quota-Domain"
	credentialReferenceHeader  = "X-CLIProxy-Credential-Ref"
	allowedAuthIDsMetadataKey  = "cliproxy.model_routing.allowed_auth_ids"
	modelRoutingMetadataKey    = "cliproxy.model_routing.fail_fast"
	routingIndexMetadataKey    = "cliproxy.model_routing.candidate.index"
	routingChannelMetadataKey  = "cliproxy.model_routing.candidate.channel"
	routingModelMetadataKey    = "cliproxy.model_routing.candidate.runtime_model_id"
	routingAliasMetadataKey    = "cliproxy.model_routing.alias"
	routingSelectorMetadataKey = "cliproxy.model_routing.route_selector"
)

type modelRoutingTable struct {
	aliases       map[string][]RoutingCandidate
	direct        map[string][]RoutingCandidate
	routes        map[string]RoutingCandidate
	variants      map[string]map[string]modelrouting.Variant
	failurePolicy modelrouting.FailurePolicy
	projected     bool
}

// RoutingCandidate is one immutable option in the already-ranked candidate chain.
// Movement between candidates is permitted only by the projection's typed failure policy.
type RoutingCandidate struct {
	Channel                string
	RuntimeModelID         string
	Alias                  string
	RouteSelector          string
	VariantID              *string
	VariantReasoningOption *string
	CredentialRefs         []modelrouting.CredentialRef
	Protocols              []string
	CatalogProviderID      string
	CanonicalModelID       string
	bootstrap              bool
}

// SetModelRouting atomically replaces the single CCS-owned routing table.
func (m *Manager) SetModelRouting(projection *modelrouting.Config) error {
	if m == nil {
		return fmt.Errorf("model routing manager is nil")
	}
	if errValidate := projection.Validate(); errValidate != nil {
		return fmt.Errorf("validate model routing projection: %w", errValidate)
	}
	if errValidate := validateRoutingVariantExecutors(projection); errValidate != nil {
		return fmt.Errorf("validate model routing variant executors: %w", errValidate)
	}
	m.modelRouting.Store(compileModelRouting(projection))
	return nil
}

func validateRoutingVariantExecutors(projection *modelrouting.Config) error {
	if projection == nil {
		return nil
	}
	for modelIndex, model := range projection.DirectModels {
		for variantIndex, variant := range model.Variants {
			if variant.ReasoningOption == nil {
				continue
			}
			for _, protocol := range variant.Protocols {
				if _, err := routingThinkingConfig(protocol, *variant.ReasoningOption); err != nil {
					return fmt.Errorf("direct-models[%d].variants[%d] protocol %q: %w", modelIndex, variantIndex, protocol, err)
				}
			}
		}
	}
	return nil
}

func compileModelRouting(projection *modelrouting.Config) *modelRoutingTable {
	table := &modelRoutingTable{
		aliases:   make(map[string][]RoutingCandidate),
		direct:    make(map[string][]RoutingCandidate),
		routes:    make(map[string]RoutingCandidate),
		variants:  make(map[string]map[string]modelrouting.Variant),
		projected: projection != nil,
	}
	if projection == nil {
		return table
	}
	table.failurePolicy = cloneRoutingFailurePolicy(projection.FailurePolicy)
	for _, model := range projection.DirectModels {
		modelID := routingModelIdentity(model.ModelKey.CatalogProviderID, model.ModelKey.CanonicalModelID)
		variants := make(map[string]modelrouting.Variant, len(model.Variants))
		for _, variant := range model.Variants {
			variants[variant.VariantKey.VariantID] = cloneRoutingVariant(variant)
		}
		table.variants[modelID] = variants
		for _, route := range model.Routes {
			candidate := RoutingCandidate{
				Channel:           route.RouteKey.RouteChannel,
				RuntimeModelID:    route.RuntimeModelID,
				RouteSelector:     route.RouteSelector,
				CredentialRefs:    cloneCredentialRefs(route.CredentialRefs),
				Protocols:         append([]string(nil), route.Protocols...),
				CatalogProviderID: route.RouteKey.CatalogProviderID,
				CanonicalModelID:  route.RouteKey.CanonicalModelID,
			}
			table.routes[route.RouteSelector] = candidate
			directKey := strings.ToLower(route.RuntimeModelID)
			table.direct[directKey] = append(table.direct[directKey], candidate)
		}
	}
	for _, alias := range projection.Aliases {
		if !alias.Selectable {
			continue
		}
		candidates := make([]RoutingCandidate, 0, len(alias.Candidates))
		for _, source := range alias.Candidates {
			candidate := RoutingCandidate{
				Channel:           source.RouteChannel,
				RuntimeModelID:    source.RuntimeModelID,
				Alias:             alias.Name,
				RouteSelector:     source.RouteSelector,
				VariantID:         cloneString(source.VariantID),
				CredentialRefs:    cloneCredentialRefs(source.CredentialRefs),
				Protocols:         append([]string(nil), source.Protocols...),
				CatalogProviderID: source.ModelKey.CatalogProviderID,
				CanonicalModelID:  source.ModelKey.CanonicalModelID,
			}
			if source.VariantID != nil {
				variant := table.variants[routingModelIdentity(source.ModelKey.CatalogProviderID, source.ModelKey.CanonicalModelID)][*source.VariantID]
				candidate.VariantReasoningOption = cloneString(variant.ReasoningOption)
			}
			candidates = append(candidates, candidate)
		}
		table.aliases[strings.ToLower(alias.Name)] = candidates
	}
	return table
}

func cloneRoutingFailurePolicy(policy modelrouting.FailurePolicy) modelrouting.FailurePolicy {
	policy.FailoverRules = append([]modelrouting.FailoverRule(nil), policy.FailoverRules...)
	for index := range policy.FailoverRules {
		policy.FailoverRules[index].HTTPStatuses = append([]int(nil), policy.FailoverRules[index].HTTPStatuses...)
		policy.FailoverRules[index].ErrorCodes = append([]string(nil), policy.FailoverRules[index].ErrorCodes...)
		policy.FailoverRules[index].FailureKinds = append([]modelrouting.FailureKind(nil), policy.FailoverRules[index].FailureKinds...)
	}
	return policy
}

func cloneRoutingVariant(value modelrouting.Variant) modelrouting.Variant {
	value.DisplayName = cloneString(value.DisplayName)
	value.ReasoningOption = cloneString(value.ReasoningOption)
	value.Protocols = append([]string(nil), value.Protocols...)
	return value
}

func cloneCredentialRefs(values []modelrouting.CredentialRef) []modelrouting.CredentialRef {
	return append([]modelrouting.CredentialRef(nil), values...)
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func routingModelIdentity(providerID, modelID string) string {
	return providerID + "\x00" + modelID
}

func cloneRoutingCandidate(candidate RoutingCandidate) RoutingCandidate {
	candidate.VariantID = cloneString(candidate.VariantID)
	candidate.VariantReasoningOption = cloneString(candidate.VariantReasoningOption)
	candidate.CredentialRefs = cloneCredentialRefs(candidate.CredentialRefs)
	candidate.Protocols = append([]string(nil), candidate.Protocols...)
	return candidate
}

func (m *Manager) modelRoutingCandidates(requestedModel string, opts cliproxyexecutor.Options) ([]RoutingCandidate, bool, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	if m == nil {
		return nil, false, nil
	}
	table := m.modelRouting.Load()
	if table == nil {
		return nil, false, nil
	}
	selector := strings.TrimSpace(opts.Headers.Get(probeRouteSelectorHeader))
	variantHeader := strings.TrimSpace(opts.Headers.Get(probeVariantIDHeader))
	if selector != "" {
		route, exists := table.routes[selector]
		if !exists && !table.projected {
			var errBootstrap error
			route, exists, errBootstrap = m.bootstrapRoutingCandidate(selector, requestedModel)
			if errBootstrap != nil {
				return nil, true, errBootstrap
			}
		}
		if !exists || route.RuntimeModelID != requestedModel {
			return nil, true, routingContractError("route_not_selectable", "route selector is not bound to the requested runtime model", http.StatusServiceUnavailable)
		}
		if variantHeader != "" {
			modelID := routingModelIdentity(route.CatalogProviderID, route.CanonicalModelID)
			variant, exists := table.variants[modelID][variantHeader]
			if !exists {
				return nil, true, routingContractError("variant_not_executable", "variant is not nested under the selected model", http.StatusUnprocessableEntity)
			}
			route.VariantID = &variantHeader
			route.VariantReasoningOption = cloneString(variant.ReasoningOption)
			if route.VariantReasoningOption == nil {
				return nil, true, routingContractError("variant_not_executable", "variant has no catalog-owned runtime option", http.StatusUnprocessableEntity)
			}
		}
		return []RoutingCandidate{cloneRoutingCandidate(route)}, true, nil
	}
	if variantHeader != "" {
		return nil, true, routingContractError("variant_not_executable", "variant header requires a route selector", http.StatusUnprocessableEntity)
	}
	key := strings.ToLower(requestedModel)
	if candidates, exists := table.aliases[key]; exists {
		return cloneRoutingCandidates(candidates), true, nil
	}
	if candidates, exists := table.direct[key]; exists {
		return cloneRoutingCandidates(candidates), true, nil
	}
	if strings.HasPrefix(key, "aihub-") {
		return nil, true, routingContractError("route_not_selectable", "managed alias is absent from the active projection", http.StatusServiceUnavailable)
	}
	return nil, false, nil
}

// bootstrapRoutingCandidate resolves one inventory selector directly from the
// live registry before CCS has published a routing projection. It never creates
// an alias, ranks routes, or permits movement to another route.
func (m *Manager) bootstrapRoutingCandidate(selector, requestedModel string) (RoutingCandidate, bool, error) {
	if m == nil {
		return RoutingCandidate{}, false, nil
	}
	var candidate RoutingCandidate
	var catalogRouteProviderID, catalogRouteModelID string
	credentialRefs := make(map[string]struct{})
	matched := false
	for index, registered := range registry.GetGlobalRegistry().RegisteredRouteSnapshots() {
		info := registered.Model
		if info == nil || registered.RuntimeModelID != requestedModel {
			continue
		}
		key := modelrouting.RouteKey{
			CatalogProviderID: strings.TrimSpace(info.CatalogProviderID),
			CanonicalModelID:  strings.TrimSpace(info.CatalogModelID),
			RouteChannel:      strings.TrimSpace(registered.RouteChannel),
		}
		if modelrouting.SelectorForRoute(key, registered.RuntimeModelID) != selector {
			continue
		}
		facts := []struct {
			name  string
			value string
		}{
			{name: "catalog provider", value: info.CatalogProviderID},
			{name: "catalog model", value: info.CatalogModelID},
			{name: "catalog route provider", value: info.CatalogRouteProviderID},
			{name: "catalog route model", value: info.CatalogRouteModelID},
			{name: "route channel", value: registered.RouteChannel},
			{name: "runtime model", value: registered.RuntimeModelID},
		}
		for _, fact := range facts {
			if !isCanonicalBootstrapFact(fact.value) {
				return RoutingCandidate{}, true, routingContractError(
					"route_not_selectable",
					fmt.Sprintf("registered route %d has invalid %s fact", index, fact.name),
					http.StatusServiceUnavailable,
				)
			}
		}
		if len(info.Protocols) == 0 {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", fmt.Sprintf("registered route %d has no explicit protocols", index), http.StatusServiceUnavailable)
		}
		for _, protocol := range info.Protocols {
			if !isCanonicalBootstrapFact(protocol) {
				return RoutingCandidate{}, true, routingContractError("route_not_selectable", fmt.Sprintf("registered route %d has an invalid protocol", index), http.StatusServiceUnavailable)
			}
		}

		if !matched {
			candidate = RoutingCandidate{
				Channel:           registered.RouteChannel,
				RuntimeModelID:    registered.RuntimeModelID,
				RouteSelector:     selector,
				Protocols:         append([]string(nil), info.Protocols...),
				CatalogProviderID: info.CatalogProviderID,
				CanonicalModelID:  info.CatalogModelID,
				bootstrap:         true,
			}
			catalogRouteProviderID = info.CatalogRouteProviderID
			catalogRouteModelID = info.CatalogRouteModelID
			matched = true
		} else if candidate.Channel != registered.RouteChannel ||
			candidate.RuntimeModelID != registered.RuntimeModelID ||
			candidate.CatalogProviderID != info.CatalogProviderID ||
			candidate.CanonicalModelID != info.CatalogModelID ||
			catalogRouteProviderID != info.CatalogRouteProviderID ||
			catalogRouteModelID != info.CatalogRouteModelID ||
			!slices.Equal(candidate.Protocols, info.Protocols) {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered routes conflict for the requested selector", http.StatusServiceUnavailable)
		}

		auth, exists := m.GetByID(registered.ClientID)
		if !exists || auth == nil {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route has no live credential", http.StatusServiceUnavailable)
		}
		if executorKeyFromAuth(auth) != candidate.Channel {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route channel differs from its credential channel", http.StatusServiceUnavailable)
		}
		ref := credentialReference(auth)
		if ref.Kind != "api_key" && ref.Kind != AuthKindOAuth {
			return RoutingCandidate{}, true, routingContractError("route_not_selectable", "registered route has an unsupported credential kind", http.StatusServiceUnavailable)
		}
		identity := ref.ID + "\x00" + ref.Kind
		if _, exists := credentialRefs[identity]; !exists {
			credentialRefs[identity] = struct{}{}
			candidate.CredentialRefs = append(candidate.CredentialRefs, ref)
		}
	}
	return candidate, matched, nil
}

func isCanonicalBootstrapFact(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && value != "unknown"
}

func cloneRoutingCandidates(values []RoutingCandidate) []RoutingCandidate {
	result := make([]RoutingCandidate, len(values))
	for index := range values {
		result[index] = cloneRoutingCandidate(values[index])
	}
	return result
}

func (m *Manager) isModelRoutingRequest(requestedModel string, opts cliproxyexecutor.Options) bool {
	_, matched, _ := m.modelRoutingCandidates(requestedModel, opts)
	return matched
}

// ModelRoutingProviders resolves only the executor channels already projected by
// CCS. API handlers use it before the legacy registry lookup so managed aliases
// reach the model-routing executor without duplicating selection policy.
func (m *Manager) ModelRoutingProviders(requestedModel string) ([]string, bool, error) {
	candidates, matched, errCandidates := m.modelRoutingCandidates(requestedModel, cliproxyexecutor.Options{})
	if errCandidates != nil {
		return nil, matched, errCandidates
	}
	if !matched {
		return nil, false, nil
	}
	providers := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		provider := strings.ToLower(strings.TrimSpace(candidate.Channel))
		if provider == "" {
			return nil, true, routingContractError("route_not_selectable", "projected candidate has no route channel", http.StatusServiceUnavailable)
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, true, routingContractError("route_not_selectable", "managed model has no projected executor channel", http.StatusServiceUnavailable)
	}
	return providers, true, nil
}

func (m *Manager) modelRoutingFailurePolicy() modelrouting.FailurePolicy {
	if m == nil {
		return modelrouting.FailurePolicy{}
	}
	table := m.modelRouting.Load()
	if table == nil {
		return modelrouting.FailurePolicy{}
	}
	return table.failurePolicy
}

func credentialReference(auth *Auth) modelrouting.CredentialRef {
	if auth == nil {
		return modelrouting.CredentialRef{}
	}
	kind := auth.AuthKind()
	if kind == AuthKindAPIKey {
		kind = "api_key"
	}
	return modelrouting.CredentialRef{ID: CredentialReferenceID(auth.ID), Kind: kind}
}

// CredentialReferenceID returns the stable secret-free identifier used by the
// management inventory and model-routing allowlists.
func CredentialReferenceID(rawID string) string {
	digest := sha256.Sum256([]byte(rawID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (m *Manager) routingCandidateKnownSelectable(ctx context.Context, candidate RoutingCandidate, opts cliproxyexecutor.Options) bool {
	if m == nil {
		return false
	}
	if _, exists := m.Executor(strings.ToLower(candidate.Channel)); !exists {
		return false
	}
	allowed := make(map[string]struct{}, len(candidate.CredentialRefs))
	for _, ref := range candidate.CredentialRefs {
		allowed[ref.ID+"\x00"+ref.Kind] = struct{}{}
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	now := time.Now()
	for _, auth := range m.snapshotAuths() {
		if auth == nil || executorKeyFromAuth(auth) != strings.ToLower(candidate.Channel) || !eligibility.allows(auth) {
			continue
		}
		ref := credentialReference(auth)
		if _, exists := allowed[ref.ID+"\x00"+ref.Kind]; !exists {
			continue
		}
		blocked, _, _ := isAuthBlockedForModel(auth, candidate.RuntimeModelID, now)
		if !blocked {
			return true
		}
	}
	return false
}

func (m *Manager) candidateOptions(
	opts cliproxyexecutor.Options,
	index int,
	candidate RoutingCandidate,
	recordSelectedAuth func(string),
) cliproxyexecutor.Options {
	headers := opts.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Del(probeRouteSelectorHeader)
	headers.Del(probeVariantIDHeader)
	opts.Headers = headers

	allowedRefs := make(map[string]struct{}, len(candidate.CredentialRefs))
	for _, ref := range candidate.CredentialRefs {
		allowedRefs[ref.ID+"\x00"+ref.Kind] = struct{}{}
	}
	allowedIDs := make(map[string]struct{})
	for _, auth := range m.snapshotAuths() {
		ref := credentialReference(auth)
		if _, allowed := allowedRefs[ref.ID+"\x00"+ref.Kind]; allowed {
			allowedIDs[auth.ID] = struct{}{}
		}
	}
	meta := make(map[string]any, len(opts.Metadata)+8)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	meta[allowedAuthIDsMetadataKey] = allowedIDs
	meta[modelRoutingMetadataKey] = true
	meta[routingIndexMetadataKey] = index
	meta[routingChannelMetadataKey] = candidate.Channel
	meta[routingModelMetadataKey] = candidate.RuntimeModelID
	meta[routingAliasMetadataKey] = candidate.Alias
	meta[routingSelectorMetadataKey] = candidate.RouteSelector
	meta[cliproxyexecutor.AuthSelectionModelMetadataKey] = candidate.RuntimeModelID
	priorSelectedAuthCallback, _ := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
		if priorSelectedAuthCallback != nil {
			priorSelectedAuthCallback(authID)
		}
		if recordSelectedAuth != nil {
			recordSelectedAuth(authID)
		}
	}
	if candidate.VariantReasoningOption != nil {
		meta[cliproxyexecutor.ReasoningEffortMetadataKey] = *candidate.VariantReasoningOption
	}
	opts.Metadata = meta
	return opts
}

func isModelRoutingOptions(opts cliproxyexecutor.Options) bool {
	value, _ := opts.Metadata[modelRoutingMetadataKey].(bool)
	return value
}

func allowedAuthIDsFromMetadata(metadata map[string]any) (map[string]struct{}, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, exists := metadata[allowedAuthIDsMetadataKey]
	if !exists {
		return nil, false
	}
	values, ok := raw.(map[string]struct{})
	return values, ok
}

func routingProtocol(format sdktranslator.Format) (string, bool) {
	switch format {
	case sdktranslator.FormatOpenAI:
		return "openai_chat", true
	case sdktranslator.FormatOpenAIResponse:
		return "openai_responses", true
	case sdktranslator.FormatClaude:
		return "anthropic_messages", true
	default:
		return "", false
	}
}

func prepareRoutingCandidateRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidate RoutingCandidate) (cliproxyexecutor.Request, error) {
	protocol, supported := routingProtocol(opts.SourceFormat)
	if !supported || !containsRoutingValue(candidate.Protocols, protocol) {
		return req, routingContractError("protocol_not_supported", fmt.Sprintf("route does not support request protocol %q", opts.SourceFormat), http.StatusBadRequest)
	}
	req.Model = candidate.RuntimeModelID
	if candidate.VariantID == nil {
		return req, nil
	}
	if candidate.VariantReasoningOption == nil {
		return req, routingContractError("variant_not_executable", "variant has no catalog-owned runtime option", http.StatusUnprocessableEntity)
	}
	if len(req.Payload) == 0 || !gjson.ValidBytes(req.Payload) || !gjson.ParseBytes(req.Payload).IsObject() {
		return req, routingContractError("variant_not_executable", "variant requires a JSON object request payload", http.StatusUnprocessableEntity)
	}
	updated, err := applyRoutingReasoningOption(req.Payload, protocol, *candidate.VariantReasoningOption)
	if err != nil {
		return req, routingContractError("variant_not_executable", err.Error(), http.StatusUnprocessableEntity)
	}
	req.Payload = updated
	return req, nil
}

func applyRoutingReasoningOption(payload []byte, protocol, option string) ([]byte, error) {
	config, errConfig := routingThinkingConfig(protocol, option)
	if errConfig != nil {
		return nil, errConfig
	}

	var updated []byte
	var err error
	switch protocol {
	case "openai_chat":
		updated, err = thinkingopenai.NewApplier().Apply(payload, config, nil)
	case "openai_responses":
		updated, err = thinkingcodex.NewApplier().Apply(payload, config, nil)
	case "anthropic_messages":
		updated, err = thinkingclaude.NewApplier().Apply(payload, config, nil)
	default:
		return nil, fmt.Errorf("no variant executor for protocol %q", protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("apply reasoning option for %s: %w", protocol, err)
	}
	return updated, nil
}

func routingThinkingConfig(protocol, option string) (thinking.ThinkingConfig, error) {
	if mode, ok := thinking.ParseSpecialSuffix(option); ok {
		switch mode {
		case thinking.ModeNone:
			return thinking.ThinkingConfig{Mode: thinking.ModeNone}, nil
		case thinking.ModeAuto:
			return thinking.ThinkingConfig{Mode: thinking.ModeAuto, Budget: -1}, nil
		}
	}
	if level, ok := thinking.ParseLevelSuffix(option); ok {
		return thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: level}, nil
	}
	if budget, errBudget := strconv.Atoi(option); errBudget == nil && budget > 0 {
		if protocol != "anthropic_messages" {
			return thinking.ThinkingConfig{}, fmt.Errorf("numeric reasoning option is not executable by protocol %q", protocol)
		}
		return thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: budget}, nil
	}
	return thinking.ThinkingConfig{}, fmt.Errorf("reasoning option %q is not executable by protocol %q", option, protocol)
}

func containsRoutingValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type routingContractFailure struct {
	cause *Error
}

func (e *routingContractFailure) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *routingContractFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *routingContractFailure) StatusCode() int {
	if e == nil || e.cause == nil {
		return 0
	}
	return e.cause.StatusCode()
}

func isRoutingContractFailure(err error) bool {
	var contractErr *routingContractFailure
	return errors.As(err, &contractErr) && contractErr != nil
}

func routingContractError(code, message string, status int) error {
	return &routingContractFailure{cause: &Error{Code: code, Message: message, HTTPStatus: status}}
}

func mergeModelRoutingHeaders(existing, routing http.Header) http.Header {
	result := existing.Clone()
	if result == nil {
		result = make(http.Header)
	}
	for key, values := range routing {
		result.Del(key)
		for _, value := range values {
			result.Add(key, value)
		}
	}
	return result
}

func (m *Manager) modelRoutingResponseHeaders(candidate RoutingCandidate, selectedAuthID string) http.Header {
	headers := make(http.Header)
	headers.Set(probeRouteSelectorHeader, candidate.RouteSelector)
	headers.Set(effectiveModelHeader, candidate.RuntimeModelID)
	if candidate.VariantID != nil {
		headers.Set(probeVariantIDHeader, *candidate.VariantID)
	}
	if selectedAuthID == "" || m == nil {
		return headers
	}
	auth, exists := m.GetByID(selectedAuthID)
	if !exists || auth == nil {
		return headers
	}
	ref := credentialReference(auth)
	if ref.ID != "" {
		headers.Set(credentialReferenceHeader, ref.ID)
	}
	if domain := credentialQuotaDomain(auth); domain != "" {
		headers.Set(quotaDomainHeader, domain)
	}
	return headers
}

func credentialQuotaDomain(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(auth.QuotaDomain)
}

type modelRoutingPointer = atomic.Pointer[modelRoutingTable]
