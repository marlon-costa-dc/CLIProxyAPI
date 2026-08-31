package management

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelrouting"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// GetModelInventory returns a route-aware secret-free snapshot. It extends the
// existing management surface; configuration writes remain CCS-owned.
func (h *Handler) GetModelInventory(c *gin.Context) {
	now := time.Now().UTC()
	h.mu.Lock()
	manager := h.authManager
	state := h.modelRoutingStateHook
	h.mu.Unlock()
	activeState := modelrouting.ActiveStateV2{}
	if state != nil {
		activeState = state()
	}
	active := activeState.Identity
	projection := activeState.Projection
	if (active == nil) != (activeState.LoadedAt == nil) || (active == nil) != (projection == nil) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "active model-routing identity and activation timestamp are inconsistent"})
		return
	}
	if projection != nil && active == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "active model-routing identity is unavailable"})
		return
	}

	auths := make(map[string]*coreauth.Auth)
	if manager != nil {
		for _, auth := range manager.List() {
			if auth != nil {
				auths[auth.ID] = auth
			}
		}
	}
	registered := registry.GetGlobalRegistry().RegisteredRouteSnapshots()
	inventory := modelrouting.Inventory{
		SchemaVersion:      modelrouting.SchemaVersion,
		GeneratedAt:        now,
		Active:             active,
		ActivationLoadedAt: activeState.LoadedAt,
		BinaryProvenance: modelrouting.BinaryProvenance{
			Version: buildinfo.Version,
			Commit:  buildinfo.Commit,
			BuiltAt: buildinfo.BuildDate,
		},
		RoutingSchema: modelrouting.RoutingSchemaInfo{Version: modelrouting.SchemaVersion, Digest: modelrouting.SchemaDigest()},
		Aliases:       []modelrouting.InventoryAlias{},
	}
	if projection != nil {
		inventory.DirectModels = projectedInventoryModels(projection, registered, auths, now)
		inventory.Aliases = projectedInventoryAliases(projection)
	} else {
		models, errBootstrap := bootstrapInventoryModels(registered, auths, now)
		if errBootstrap != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("build model inventory: %v", errBootstrap)})
			return
		}
		inventory.DirectModels = models
	}
	if inventory.DirectModels == nil {
		inventory.DirectModels = []modelrouting.InventoryModel{}
	}
	c.JSON(http.StatusOK, inventory)
}

func projectedInventoryModels(projection *modelrouting.Config, registered []registry.RegisteredRouteSnapshot, auths map[string]*coreauth.Auth, now time.Time) []modelrouting.InventoryModel {
	byRoute := make(map[string][]registry.RegisteredRouteSnapshot)
	for _, route := range registered {
		key := route.RouteChannel + "\x00" + route.RuntimeModelID
		byRoute[key] = append(byRoute[key], route)
	}
	models := make([]modelrouting.InventoryModel, 0, len(projection.DirectModels))
	for _, direct := range projection.DirectModels {
		model := modelrouting.InventoryModel{
			ModelKey: modelKeyJSON(direct.ModelKey), DisplayName: direct.DisplayName,
			Variants: make([]modelrouting.InventoryVariant, 0, len(direct.Variants)),
			Routes:   make([]modelrouting.InventoryRoute, 0, len(direct.Routes)),
		}
		for _, variant := range direct.Variants {
			model.Variants = append(model.Variants, modelrouting.InventoryVariant{
				VariantKey: modelrouting.VariantKeyJSON{
					ModelKey:  modelKeyJSON(variant.VariantKey.ModelKey),
					VariantID: variant.VariantKey.VariantID,
				},
				DisplayName: cloneOptionalString(variant.DisplayName),
				Protocols:   append([]string(nil), variant.Protocols...),
			})
		}
		for _, directRoute := range direct.Routes {
			allowed := make(map[string]struct{}, len(directRoute.CredentialRefs))
			for _, ref := range directRoute.CredentialRefs {
				allowed[ref.ID+"\x00"+ref.Kind] = struct{}{}
			}
			credentials := make([]modelrouting.InventoryCredential, 0, len(directRoute.CredentialRefs))
			for _, registeredRoute := range byRoute[directRoute.RouteKey.RouteChannel+"\x00"+directRoute.RuntimeModelID] {
				credential := inventoryCredential(registeredRoute, auths[registeredRoute.ClientID], now)
				identity := credential.CredentialRef.ID + "\x00" + credential.CredentialRef.Kind
				if _, exists := allowed[identity]; exists {
					credentials = append(credentials, credential)
				}
			}
			sortCredentials(credentials)
			selectable := directRoute.Selectable && hasSelectableCredential(credentials)
			reason := directRoute.SelectionReason
			if !selectable && directRoute.Selectable {
				reason = "no projected credential is currently selectable"
			}
			health := inventoryHealth(directRoute.Health)
			health.Selectable = health.Selectable && selectable
			if !health.Selectable && health.Status == "healthy" {
				health.Status = "blocked"
			}
			model.Routes = append(model.Routes, modelrouting.InventoryRoute{
				RouteKey: modelrouting.RouteKeyJSON{
					ModelKey:     modelKeyJSON(directRoute.RouteKey.ModelKey),
					RouteChannel: directRoute.RouteKey.RouteChannel,
				},
				CatalogRouteProviderID: directRoute.CatalogRouteProviderID,
				CatalogRouteModelID:    directRoute.CatalogRouteModelID,
				RuntimeModelID:         directRoute.RuntimeModelID,
				RouteSelector:          directRoute.RouteSelector,
				QuotaDomains:           append([]string(nil), directRoute.QuotaDomains...),
				Protocols:              append([]string(nil), directRoute.Protocols...),
				Restrictions:           inventoryRestrictions(directRoute.Restrictions),
				Health:                 health,
				Selectable:             selectable,
				SelectionReason:        reason,
				Credentials:            credentials,
			})
			model.Active = model.Active || selectable
		}
		models = append(models, model)
	}
	return models
}

func projectedInventoryAliases(projection *modelrouting.Config) []modelrouting.InventoryAlias {
	aliases := make([]modelrouting.InventoryAlias, 0, len(projection.Aliases))
	for _, sourceAlias := range projection.Aliases {
		alias := modelrouting.InventoryAlias{
			Name: sourceAlias.Name, TierID: sourceAlias.TierID, Selectable: sourceAlias.Selectable,
			Reason: sourceAlias.Reason, Members: make([]modelrouting.InventoryMember, 0, len(sourceAlias.Members)),
		}
		for _, sourceMember := range sourceAlias.Members {
			member := modelrouting.InventoryMember{
				ModelKey: modelKeyJSON(sourceMember.ModelKey), MemberRank: sourceMember.MemberRank,
				ModelScore: sourceMember.ModelScore, SelectionReason: sourceMember.SelectionReason,
				Candidates: make([]modelrouting.InventoryCandidate, 0, len(sourceMember.Candidates)),
			}
			for _, source := range sourceMember.Candidates {
				refs := make([]modelrouting.InventoryCredentialRef, len(source.CredentialRefs))
				for index, ref := range source.CredentialRefs {
					refs[index] = modelrouting.InventoryCredentialRef{ID: ref.ID, Kind: ref.Kind}
				}
				member.Candidates = append(member.Candidates, modelrouting.InventoryCandidate{
					RouteKey: modelrouting.RouteKeyJSON{
						ModelKey: modelKeyJSON(source.RouteKey.ModelKey), RouteChannel: source.RouteKey.RouteChannel,
					},
					CatalogRouteProviderID: source.CatalogRouteProviderID,
					CatalogRouteModelID:    source.CatalogRouteModelID, RuntimeModelID: source.RuntimeModelID,
					RouteSelector: source.RouteSelector, VariantID: cloneOptionalString(source.VariantID), RouteRank: source.RouteRank,
					QuotaDomains: append([]string(nil), source.QuotaDomains...), CredentialRefs: refs,
					Protocols: append([]string(nil), source.Protocols...), Health: inventoryHealth(source.Health),
					Restrictions: inventoryRestrictions(source.Restrictions), Pricing: inventoryPricing(source.Pricing),
					SelectionReason: source.SelectionReason,
				})
			}
			alias.Members = append(alias.Members, member)
		}
		aliases = append(aliases, alias)
	}
	return aliases
}

type bootstrapModel struct {
	model  modelrouting.InventoryModel
	routes map[string]*modelrouting.InventoryRoute
}

func bootstrapInventoryModels(registered []registry.RegisteredRouteSnapshot, auths map[string]*coreauth.Auth, now time.Time) ([]modelrouting.InventoryModel, error) {
	models := make(map[string]*bootstrapModel)
	for routeIndex, registeredRoute := range registered {
		// A managed alias is a projection leak and stays fatal regardless of how
		// the route declares its catalog identity.
		if registeredRoute.Model != nil && strings.HasPrefix(strings.ToLower(registeredRoute.RuntimeModelID), "aihub-") {
			return nil, fmt.Errorf("registered route %d contains a managed alias before projection", routeIndex)
		}
		// Routes whose type has no config keys for catalog facts (claude-api-key,
		// xai, gemini, codex) are not catalog-declared, but the route is real:
		// its credential is live and the pipeline's models.dev source owns the
		// canonical identity for it. Surface it under channel identity instead
		// of hiding it, which would strand generation one.
		if registeredRoute.Model != nil && registeredRoute.CatalogDeclarationOf() == registry.CatalogDeclarationNone {
			channel := registeredRoute.RouteChannel
			modelKey := modelrouting.ModelKeyJSON{
				CatalogProviderID: channel,
				CanonicalModelID:  registeredRoute.RuntimeModelID,
			}
			modelID := channel + "\x00" + registeredRoute.RuntimeModelID
			built := models[modelID]
			if built == nil {
				display := strings.TrimSpace(registeredRoute.Model.DisplayName)
				if display == "" {
					display = registeredRoute.RuntimeModelID
				}
				built = &bootstrapModel{model: modelrouting.InventoryModel{
					ModelKey: modelKey, DisplayName: display,
					Variants: []modelrouting.InventoryVariant{}, Routes: []modelrouting.InventoryRoute{},
				}, routes: make(map[string]*modelrouting.InventoryRoute)}
				models[modelID] = built
			}
			routeChannel := registeredRoute.RouteChannel
			routeID := routeChannel + "\x00" + registeredRoute.RuntimeModelID
			if built.routes[routeID] != nil {
				continue
			}
			auth := auths[registeredRoute.ClientID]
			if auth == nil {
				return nil, fmt.Errorf("registered route %d has no matching credential", routeIndex)
			}
			if auth.QuotaDomain == "" || strings.TrimSpace(auth.QuotaDomain) != auth.QuotaDomain || auth.QuotaDomain == "unknown" {
				return nil, fmt.Errorf("registered route %d has no canonical quota domain", routeIndex)
			}
			if kind := inventoryAuthKind(auth.AuthKind()); kind != "api_key" && kind != "oauth" {
				return nil, fmt.Errorf("registered route %d has no supported credential kind", routeIndex)
			}
			route := &modelrouting.InventoryRoute{
				RouteKey: modelrouting.RouteKeyJSON{
					ModelKey: modelKey, RouteChannel: routeChannel,
				},
				CatalogRouteProviderID: channel,
				CatalogRouteModelID:    registeredRoute.RuntimeModelID,
				RuntimeModelID:         registeredRoute.RuntimeModelID,
				RouteSelector: modelrouting.SelectorForRoute(
					modelrouting.RouteKey{
						ModelKey: modelrouting.ModelKey{
							CatalogProviderID: channel, CanonicalModelID: registeredRoute.RuntimeModelID,
						},
						RouteChannel: routeChannel,
					},
					registeredRoute.RuntimeModelID,
				),
				Protocols:       []string{},
				Restrictions:    []modelrouting.InventoryRestriction{},
				Credentials:     []modelrouting.InventoryCredential{},
				SelectionReason: "route surfaced from its channel without catalog declaration",
			}
			built.model.Routes = append(built.model.Routes, *route)
			built.routes[routeID] = &built.model.Routes[len(built.model.Routes)-1]
			credential := inventoryCredential(registeredRoute, auth, now)
			built.model.Routes[len(built.model.Routes)-1].Credentials = append(
				built.model.Routes[len(built.model.Routes)-1].Credentials, credential)
			continue
		}
		routeKey, errRoute := registeredRoute.ModelRoutingKey(routeIndex)
		if errRoute != nil {
			return nil, errRoute
		}
		info := registeredRoute.Model
		auth := auths[registeredRoute.ClientID]
		if auth == nil {
			return nil, fmt.Errorf("registered route %d has no matching credential", routeIndex)
		}
		if auth.QuotaDomain == "" || strings.TrimSpace(auth.QuotaDomain) != auth.QuotaDomain || auth.QuotaDomain == "unknown" {
			return nil, fmt.Errorf("registered route %d has no canonical quota domain", routeIndex)
		}
		if kind := inventoryAuthKind(auth.AuthKind()); kind != "api_key" && kind != "oauth" {
			return nil, fmt.Errorf("registered route %d has no supported credential kind", routeIndex)
		}
		canonicalModelID := routeKey.ModelKey.CanonicalModelID
		catalogProviderID := routeKey.ModelKey.CatalogProviderID
		modelKey := modelrouting.ModelKeyJSON{CatalogProviderID: catalogProviderID, CanonicalModelID: canonicalModelID}
		modelID := catalogProviderID + "\x00" + canonicalModelID
		built := models[modelID]
		if built == nil {
			display := strings.TrimSpace(info.DisplayName)
			if display == "" {
				display = canonicalModelID
			}
			built = &bootstrapModel{model: modelrouting.InventoryModel{
				ModelKey: modelKey, DisplayName: display,
				Variants: []modelrouting.InventoryVariant{}, Routes: []modelrouting.InventoryRoute{},
			}, routes: make(map[string]*modelrouting.InventoryRoute)}
			models[modelID] = built
		}
		if variantID := strings.TrimSpace(info.VariantID); variantID != "" {
			appendBootstrapVariant(&built.model, info, variantID, append([]string(nil), info.Protocols...))
		}
		routeChannel := registeredRoute.RouteChannel
		routeID := routeChannel + "\x00" + registeredRoute.RuntimeModelID
		route := built.routes[routeID]
		if route == nil {
			protocols := append([]string(nil), info.Protocols...)
			route = &modelrouting.InventoryRoute{
				RouteKey: modelrouting.RouteKeyJSON{
					ModelKey: modelKey, RouteChannel: routeChannel,
				},
				CatalogRouteProviderID: info.CatalogRouteProviderID,
				CatalogRouteModelID:    info.CatalogRouteModelID,
				RuntimeModelID:         registeredRoute.RuntimeModelID,
				RouteSelector:          modelrouting.SelectorForRoute(routeKey, registeredRoute.RuntimeModelID),
				Protocols:              protocols,
				Restrictions:           []modelrouting.InventoryRestriction{},
				Credentials:            []modelrouting.InventoryCredential{},
				SelectionReason:        "route facts were discovered from the runtime registry",
			}
			built.model.Routes = append(built.model.Routes, *route)
			route = &built.model.Routes[len(built.model.Routes)-1]
			built.routes[routeID] = route
		} else if route.CatalogRouteProviderID != info.CatalogRouteProviderID ||
			route.CatalogRouteModelID != info.CatalogRouteModelID ||
			!equalStrings(route.Protocols, info.Protocols) {
			return nil, fmt.Errorf("registered route %d conflicts with an existing catalog route", routeIndex)
		}
		credential := inventoryCredential(registeredRoute, auth, now)
		route.Credentials = append(route.Credentials, credential)
	}

	result := make([]modelrouting.InventoryModel, 0, len(models))
	for _, built := range models {
		for index := range built.model.Routes {
			route := &built.model.Routes[index]
			sortCredentials(route.Credentials)
			route.QuotaDomains = quotaDomains(route.Credentials)
			if containsString(route.QuotaDomains, "unknown") {
				return nil, fmt.Errorf("route %s has an unknown quota domain", route.RouteKey.RouteChannel)
			}
			route.Selectable = hasSelectableCredential(route.Credentials)
			route.Health = modelrouting.InventoryHealth{
				Status: boolStatus(route.Selectable, "healthy", "unknown"), Selectable: route.Selectable,
				ObservedAt: now.Format(time.RFC3339Nano), LatencyMS: nil,
			}
			if !route.Selectable {
				route.SelectionReason = "no credential is currently selectable"
			}
			built.model.Active = built.model.Active || route.Selectable
		}
		sort.Slice(built.model.Routes, func(i, j int) bool {
			return built.model.Routes[i].RouteKey.RouteChannel < built.model.Routes[j].RouteKey.RouteChannel
		})
		sort.Slice(built.model.Variants, func(i, j int) bool {
			return built.model.Variants[i].VariantKey.VariantID < built.model.Variants[j].VariantKey.VariantID
		})
		result = append(result, built.model)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].ModelKey, result[j].ModelKey
		if left.CatalogProviderID != right.CatalogProviderID {
			return left.CatalogProviderID < right.CatalogProviderID
		}
		return left.CanonicalModelID < right.CanonicalModelID
	})
	return result, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func inventoryCredential(route registry.RegisteredRouteSnapshot, auth *coreauth.Auth, now time.Time) modelrouting.InventoryCredential {
	refID := coreauth.CredentialReferenceID(route.ClientID)
	kind := "unknown"
	if auth != nil {
		kind = inventoryAuthKind(auth.AuthKind())
	}
	suspended := strings.TrimSpace(route.SuspensionReason) != ""
	suspensionReason := optionalTrimmed(route.SuspensionReason)
	quotaBlocked := route.QuotaBlocked
	var quotaReason, remaining, resetsAt *string
	observedAt := route.LastUpdated
	selectable := auth != nil
	status := "unknown"
	quotaStatus := "unknown"
	if auth != nil {
		if auth.UpdatedAt.After(observedAt) {
			observedAt = auth.UpdatedAt
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled {
			suspended = true
			if suspensionReason == nil {
				suspensionReason = optionalTrimmed(auth.StatusMessage)
			}
		}
		quota := auth.Quota
		if modelState := auth.ModelStates[route.RuntimeModelID]; modelState != nil {
			quota = modelState.Quota
			if modelState.Unavailable || modelState.Status == coreauth.StatusDisabled {
				suspended = true
				if suspensionReason == nil {
					suspensionReason = optionalTrimmed(modelState.StatusMessage)
				}
			}
		}
		if quota.Exceeded {
			quotaBlocked = true
			quotaReason = optionalTrimmed(quota.Reason)
			if !quota.NextRecoverAt.IsZero() {
				value := quota.NextRecoverAt.UTC().Format(time.RFC3339Nano)
				resetsAt = &value
			}
		}
		if value := strings.TrimSpace(quota.Signals["remaining"]); value != "" {
			remaining = &value
		}
		selectable = selectable && auth.Status == coreauth.StatusActive && !auth.Unavailable && !suspended && !quotaBlocked
		status = boolStatus(selectable, "healthy", "blocked")
		quotaStatus = boolStatus(quotaBlocked, "blocked", "available")
	}
	if route.QuotaResetsAt != nil && resetsAt == nil {
		value := route.QuotaResetsAt.UTC().Format(time.RFC3339Nano)
		resetsAt = &value
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	return modelrouting.InventoryCredential{
		CredentialRef: modelrouting.InventoryCredentialRef{ID: refID, Kind: kind},
		QuotaDomain:   credentialQuotaDomainForInventory(auth),
		Health: modelrouting.InventoryHealth{
			Status: status, Selectable: selectable, ObservedAt: observedAt.UTC().Format(time.RFC3339Nano), LatencyMS: nil,
		},
		Quota: modelrouting.InventoryQuota{
			Status: quotaStatus, Remaining: remaining, ResetsAt: resetsAt, Reason: quotaReason,
		},
		Suspension: modelrouting.InventorySuspension{
			Active: suspended, Reason: suspensionReason, ResumesAt: nil,
		},
		Restrictions: []modelrouting.InventoryRestriction{},
	}
}

func appendBootstrapVariant(model *modelrouting.InventoryModel, info *registry.ModelInfo, variantID string, protocols []string) {
	for _, variant := range model.Variants {
		if variant.VariantKey.VariantID == variantID {
			return
		}
	}
	display := strings.TrimSpace(info.DisplayName)
	model.Variants = append(model.Variants, modelrouting.InventoryVariant{
		VariantKey: modelrouting.VariantKeyJSON{
			ModelKey:  model.ModelKey,
			VariantID: variantID,
		},
		DisplayName: optionalTrimmed(display), Protocols: protocols,
	})
}

func inventoryRestrictions(values []modelrouting.Restriction) []modelrouting.InventoryRestriction {
	result := make([]modelrouting.InventoryRestriction, 0, len(values))
	for _, value := range values {
		result = append(result, modelrouting.InventoryRestriction{
			RuleID: value.RuleID, ConfigPath: value.ConfigPath, Active: value.Active, Reason: value.Reason,
		})
	}
	return result
}

func inventoryHealth(value modelrouting.Health) modelrouting.InventoryHealth {
	return modelrouting.InventoryHealth{
		Status: value.Status, Selectable: value.Selectable, ObservedAt: value.ObservedAt, LatencyMS: cloneInt64(value.LatencyMS),
	}
}

func inventoryPricing(value *modelrouting.Pricing) *modelrouting.InventoryPricing {
	if value == nil {
		return nil
	}
	result := &modelrouting.InventoryPricing{
		Currency: value.Currency,
		Unit:     value.Unit,
		SourceID: value.SourceID,
		Entries:  make([]modelrouting.InventoryPricingEntry, len(value.Entries)),
	}
	for index, entry := range value.Entries {
		result.Entries[index] = modelrouting.InventoryPricingEntry{
			Name:       entry.Name,
			Amount:     entry.Amount,
			TierType:   cloneOptionalString(entry.TierType),
			TierSize:   cloneInt64(entry.TierSize),
			ContextKey: cloneOptionalString(entry.ContextKey),
		}
	}
	return result
}

func modelKeyJSON(value modelrouting.ModelKey) modelrouting.ModelKeyJSON {
	return modelrouting.ModelKeyJSON{CatalogProviderID: value.CatalogProviderID, CanonicalModelID: value.CanonicalModelID}
}

func credentialQuotaDomainForInventory(auth *coreauth.Auth) string {
	if auth != nil && strings.TrimSpace(auth.QuotaDomain) != "" {
		return strings.TrimSpace(auth.QuotaDomain)
	}
	return "unknown"
}

func inventoryAuthKind(value string) string {
	if value == coreauth.AuthKindAPIKey {
		return "api_key"
	}
	if value == coreauth.AuthKindOAuth {
		return "oauth"
	}
	return canonicalFact(value)
}

func quotaDomains(credentials []modelrouting.InventoryCredential) []string {
	values := make([]string, 0, len(credentials))
	for _, credential := range credentials {
		values = append(values, credential.QuotaDomain)
	}
	sort.Strings(values)
	return uniqueStrings(values)
}

func sortCredentials(credentials []modelrouting.InventoryCredential) {
	sort.Slice(credentials, func(i, j int) bool {
		if credentials[i].CredentialRef.ID != credentials[j].CredentialRef.ID {
			return credentials[i].CredentialRef.ID < credentials[j].CredentialRef.ID
		}
		return credentials[i].CredentialRef.Kind < credentials[j].CredentialRef.Kind
	})
}

func hasSelectableCredential(credentials []modelrouting.InventoryCredential) bool {
	for _, credential := range credentials {
		if credential.Health.Selectable && credential.Quota.Status != "blocked" && !credential.Suspension.Active {
			return true
		}
	}
	return false
}

func canonicalFact(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func optionalTrimmed(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func boolStatus(value bool, whenTrue, whenFalse string) string {
	if value {
		return whenTrue
	}
	return whenFalse
}
