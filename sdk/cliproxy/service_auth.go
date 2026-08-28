package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

// newDefaultAuthManager creates a default authentication manager with supported OAuth providers.
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
		sdkAuth.NewGitLabAuthenticator(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	ctx = coreauth.WithSkipPersist(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			updates := []watcher.AuthUpdate{update}
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					updates = append(updates, nextUpdate)
				default:
					break labelDrain
				}
			}
			if errUpdate := s.handleAuthUpdates(ctx, updates); errUpdate != nil {
				s.reportRuntimeError(fmt.Errorf("apply queued auth updates: %w", errUpdate))
				return
			}
		}
	}
}

func (s *Service) emitAuthUpdate(ctx context.Context, update watcher.AuthUpdate) error {
	if s == nil {
		return fmt.Errorf("emit auth update: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.watcher != nil && s.watcher.DispatchRuntimeAuthUpdate(update) {
		return nil
	}
	if s.authUpdates != nil {
		select {
		case s.authUpdates <- update:
			return nil
		default:
			log.Debugf("auth update queue saturated, applying inline action=%v id=%s", update.Action, update.ID)
		}
	}
	return s.handleAuthUpdate(ctx, update)
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) error {
	return s.handleAuthUpdates(ctx, []watcher.AuthUpdate{update})
}

func (s *Service) handleAuthUpdates(ctx context.Context, updates []watcher.AuthUpdate) error {
	if s == nil {
		return fmt.Errorf("handle auth updates: service is nil")
	}
	updates = coalesceAuthUpdates(updates)
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		return fmt.Errorf("handle auth updates: config or auth manager is nil")
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(updates))
	needsPluginSync := false
	needsAliasRebuild := false
	for _, update := range updates {
		switch update.Action {
		case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
			if update.Auth == nil || update.Auth.ID == "" {
				return fmt.Errorf("handle auth update %v: auth and auth ID are required", update.Action)
			}
			auth, errPrepare := s.prepareCoreAuthForModelRegistration(registrationCtx, update.Auth)
			if errPrepare != nil {
				return errPrepare
			}
			if auth == nil {
				return fmt.Errorf("handle auth update %v for %s: no prepared auth", update.Action, update.Auth.ID)
			}
			needsAliasRebuild = true
			authForRegistration := auth
			tasks = append(tasks, modelRegistrationTask{
				phase:    modelRegistrationPhase(authForRegistration),
				category: modelRegistrationCategory(authForRegistration),
				run: func(compatCache *openAICompatibilityRegistrationCache) error {
					return s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
				},
			})
			needsPluginSync = true
		case watcher.AuthUpdateActionDelete:
			id := update.ID
			if id == "" && update.Auth != nil {
				id = update.Auth.ID
			}
			if id == "" {
				return fmt.Errorf("handle auth delete: auth ID is required")
			}
			if errRemove := s.applyCoreAuthRemoval(registrationCtx, id); errRemove != nil {
				return errRemove
			}
			needsAliasRebuild = true
		default:
			return fmt.Errorf("handle auth update: unknown action %v", update.Action)
		}
	}

	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	if errRegister := s.runModelRegistrationTasks(registrationCtx, tasks); errRegister != nil {
		return errRegister
	}
	if needsPluginSync {
		if errSync := s.syncPluginRuntime(registrationCtx); errSync != nil {
			return fmt.Errorf("sync plugin runtime after auth updates: %w", errSync)
		}
	}
	return nil
}

func (s *Service) reportRuntimeError(err error) {
	if s == nil || err == nil {
		return
	}
	s.runtimeErrMu.RLock()
	runtimeErr := s.runtimeErr
	s.runtimeErrMu.RUnlock()
	if runtimeErr == nil {
		log.WithError(err).Error("runtime mutation failed outside an active service run")
		return
	}
	select {
	case runtimeErr <- err:
	default:
	}
}

func coalesceAuthUpdates(updates []watcher.AuthUpdate) []watcher.AuthUpdate {
	if len(updates) <= 1 {
		return updates
	}
	order := make([]string, 0, len(updates))
	byID := make(map[string]watcher.AuthUpdate, len(updates))
	unkeyed := make([]watcher.AuthUpdate, 0)
	for _, update := range updates {
		id := authUpdateID(update)
		if id == "" {
			unkeyed = append(unkeyed, update)
			continue
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = update
	}
	if len(byID) == 0 {
		return unkeyed
	}
	out := make([]watcher.AuthUpdate, 0, len(byID)+len(unkeyed))
	for _, id := range order {
		out = append(out, byID[id])
	}
	out = append(out, unkeyed...)
	return out
}

func authUpdateID(update watcher.AuthUpdate) string {
	if strings.TrimSpace(update.ID) != "" {
		return strings.TrimSpace(update.ID)
	}
	if update.Auth != nil {
		return strings.TrimSpace(update.Auth.ID)
	}
	return ""
}

func (s *Service) ensureWebsocketGateway() {
	if s == nil {
		return
	}
	if s.wsGateway != nil {
		return
	}
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    s.wsOnConnected,
		OnDisconnected: s.wsOnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	s.wsGateway = wsrelay.NewManager(opts)
}

func (s *Service) wsOnConnected(channelID string) {
	if s == nil || channelID == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(channelID), "aistudio-") {
		return
	}
	if s.coreManager != nil {
		if existing, ok := s.coreManager.GetByID(channelID); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         channelID,  // keep channel identifier as ID
		Provider:   "aistudio", // logical provider for switch routing
		Label:      channelID,  // display original channel id
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"email": channelID}, // metadata drives logging and usage tracking
	}
	log.Infof("websocket provider connected: %s", channelID)
	if errUpdate := s.emitAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	}); errUpdate != nil {
		s.reportRuntimeError(errUpdate)
	}
}

func (s *Service) wsOnDisconnected(channelID string, reason error) {
	if s == nil || channelID == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", channelID)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", channelID, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", channelID)
	}
	ctx := context.Background()
	if errUpdate := s.emitAuthUpdate(ctx, watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     channelID,
	}); errUpdate != nil {
		s.reportRuntimeError(errUpdate)
	}
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) error {
	auth, errPrepare := s.prepareCoreAuthForModelRegistration(ctx, auth)
	if errPrepare != nil {
		return errPrepare
	}
	if auth == nil {
		return nil
	}
	if errRegister := s.completeModelRegistrationForAuth(ctx, auth); errRegister != nil {
		return errRegister
	}
	if errSync := s.syncPluginRuntime(ctx); errSync != nil {
		return fmt.Errorf("sync plugin runtime after auth update: %w", errSync)
	}
	return nil
}

func (s *Service) prepareCoreAuthForModelRegistration(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return nil, nil
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuthWithContext(ctx, auth, false)

	// IMPORTANT: Update coreManager FIRST, before model registration.
	// This ensures that configuration changes (proxy_url, prefix, etc.) take effect
	// immediately for API calls, rather than waiting for model registration to complete.
	op := "register"
	var err error
	if existing, ok := s.coreManager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled && !auth.Disabled && auth.Status != coreauth.StatusDisabled {
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRefreshAfter = existing.NextRefreshAfter
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
		}
		op = "update"
		_, err = s.coreManager.Update(ctx, auth)
	} else {
		_, err = s.coreManager.Register(ctx, auth)
	}
	if err != nil {
		GlobalModelRegistry().UnregisterClient(auth.ID)
		return nil, fmt.Errorf("%s auth %s: %w", op, auth.ID, err)
	}
	return auth, nil
}

func (s *Service) completeModelRegistrationForAuth(ctx context.Context, auth *coreauth.Auth) error {
	return s.completeModelRegistrationForAuthWithCache(ctx, auth, nil)
}

func (s *Service) completeModelRegistrationForAuthWithCache(ctx context.Context, auth *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) error {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errRegister := s.registerModelsForAuthWithCache(ctx, auth, compatCache); errRegister != nil {
		return fmt.Errorf("register models for auth %s: %w", auth.ID, errRegister)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)

	// Refresh the scheduler entry so that the auth's supportedModelSet is rebuilt
	// from the now-populated global model registry. Without this, newly added auths
	// have an empty supportedModelSet (because Register/Update upserts into the
	// scheduler before registerModelsForAuth runs) and are invisible to the scheduler.
	s.coreManager.RefreshSchedulerEntry(auth.ID)
	return nil
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) error {
	if s == nil || id == "" {
		return fmt.Errorf("remove core auth: service and auth ID are required")
	}
	if s.coreManager == nil {
		return fmt.Errorf("remove core auth %s: auth manager is nil", id)
	}
	id = strings.TrimSpace(id)
	var provider string
	if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
		provider = strings.TrimSpace(existing.Provider)
	}
	errRemove := s.coreManager.Remove(ctx, id)
	GlobalModelRegistry().UnregisterClient(id)
	if strings.EqualFold(provider, "codex") {
		executor.CloseCodexWebsocketSessionsForAuthID(id, "auth_removed")
	}
	if strings.EqualFold(provider, "xai") {
		executor.CloseXAIWebsocketSessionsForAuthID(id, "auth_removed")
	}
	errSync := s.syncPluginRuntime(ctx)
	if errRemove != nil {
		errRemove = fmt.Errorf("remove core auth %s: %w", id, errRemove)
	}
	if errSync != nil {
		errSync = fmt.Errorf("sync plugin runtime after removing auth %s: %w", id, errSync)
	}
	return errors.Join(errRemove, errSync)
}

func (s *Service) applyRetryConfig(cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	maxInterval := time.Duration(cfg.MaxRetryInterval) * time.Second
	s.coreManager.SetRetryConfig(cfg.RequestRetry, maxInterval, cfg.MaxRetryCredentials)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
}

func (s *Service) configureCooldownStateStore(cfg *config.Config) error {
	return s.configureCooldownStateStoreContext(context.Background(), cfg, false)
}

func (s *Service) configureCooldownStateStoreContext(ctx context.Context, cfg *config.Config, persistOld bool) error {
	if s == nil || s.coreManager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	store, errResolve := s.resolveCooldownStateStore(cfg)
	if errResolve != nil {
		return errResolve
	}
	return s.coreManager.SwapCooldownStateStore(ctx, store, persistOld)
}

func (s *Service) resolveCooldownStateStore(cfg *config.Config) (coreauth.CooldownStateStore, error) {
	if cfg == nil || !cfg.SaveCooldownStatus || cfg.Home.Enabled {
		return nil, nil
	}
	if s != nil && s.cooldownStateStore != nil {
		return s.cooldownStateStore, nil
	}
	authDir, errResolve := resolveCooldownStateAuthDir(cfg)
	if errResolve != nil {
		return nil, fmt.Errorf("resolve cooldown state directory: %w", errResolve)
	}
	if authDir == "" {
		return nil, nil
	}
	return coreauth.NewFileCooldownStateStoreWithAuthDir(authDir, authDir), nil
}

func resolveCooldownStateAuthDir(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", nil
	}
	authDir, errAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errAuthDir != nil {
		return "", errAuthDir
	}
	return authDir, nil
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if routeChannel := strings.TrimSpace(a.RouteChannel); routeChannel != "" {
		if len(a.Attributes) > 0 {
			compatName = strings.TrimSpace(a.Attributes["compat_name"])
		}
		return routeChannel, compatName, true
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		compatName = strings.TrimSpace(a.Label)
		providerKey = compatName
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey), compatName, true
	}
	return "", "", false
}
