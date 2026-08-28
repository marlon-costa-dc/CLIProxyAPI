package cliproxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func (s *Service) applyConfigUpdate(newCfg *config.Config) {
	if errApply := s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, true); errApply != nil {
		s.reportRuntimeError(fmt.Errorf("config update failed: %w", errApply))
	}
}

func (s *Service) applyWatcherConfigUpdate(newCfg *config.Config) {
	if errApply := s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, false); errApply != nil {
		s.reportRuntimeError(fmt.Errorf("watcher config update failed: %w", errApply))
	}
}

type configCommit struct {
	cfg      *config.Config
	sequence uint64
}

type routingRuntimeState struct {
	strategy           string
	sessionAffinity    bool
	sessionAffinityTTL time.Duration
}

func normalizedRoutingRuntimeState(cfg *config.Config) routingRuntimeState {
	state := routingRuntimeState{
		strategy:           "round-robin",
		sessionAffinityTTL: time.Hour,
	}
	if cfg == nil {
		return state
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		state.strategy = "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		state.strategy = "fill-first"
	}
	state.sessionAffinity = cfg.Routing.SessionAffinity
	if ttl := strings.TrimSpace(cfg.Routing.SessionAffinityTTL); ttl != "" {
		if parsed, errParse := time.ParseDuration(ttl); errParse == nil && parsed > 0 {
			state.sessionAffinityTTL = parsed
		}
	}
	return state
}

func newRoutingSelector(state routingRuntimeState) coreauth.Selector {
	var selector coreauth.Selector
	switch state.strategy {
	case "weighted-round-robin":
		selector = &coreauth.WeightedRoundRobinSelector{}
	case "fill-first":
		selector = &coreauth.FillFirstSelector{}
	default:
		selector = &coreauth.RoundRobinSelector{}
	}
	if state.sessionAffinity {
		selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback: selector,
			TTL:      state.sessionAffinityTTL,
		})
	}
	return selector
}

func (s *Service) applyConfigUpdateWithAuthSynthesis(ctx context.Context, newCfg *config.Config, synthesizeConfigAuths bool) error {
	if s == nil {
		return fmt.Errorf("apply config update: service is nil")
	}
	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if errRouting := s.validateAppliedModelRouting(newCfg); errRouting != nil {
		return fmt.Errorf("validate model-routing ownership: %w", errRouting)
	}
	commit, errCommit := s.commitConfigUpdate(newCfg)
	if errCommit != nil {
		return errCommit
	}
	return s.applyConfigRuntime(ctx, commit, synthesizeConfigAuths)
}

// commitConfigUpdate applies only in-memory configuration state. Runtime work that
// may block on plugins, models, storage, or networking is deliberately deferred.
func (s *Service) commitConfigUpdate(newCfg *config.Config) (configCommit, error) {
	if s == nil {
		return configCommit{}, fmt.Errorf("commit config update: service is nil")
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if newCfg == nil {
		return configCommit{}, fmt.Errorf("commit config update: config is nil")
	}
	if errValidate := newCfg.ValidateRuntimeConfig(); errValidate != nil {
		return configCommit{}, fmt.Errorf("validate config update: %w", errValidate)
	}

	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	s.configSequence++
	return configCommit{cfg: newCfg, sequence: s.configSequence}, nil
}

func (s *Service) configCommitCurrent(commit configCommit) bool {
	if s == nil || commit.sequence == 0 {
		return false
	}
	s.configUpdateMu.Lock()
	current := s.configSequence == commit.sequence
	s.configUpdateMu.Unlock()
	return current
}

func (s *Service) applyConfigRuntime(ctx context.Context, commit configCommit, synthesizeConfigAuths bool) error {
	cfg := commit.cfg
	if s == nil || cfg == nil {
		return fmt.Errorf("apply config runtime: service or config is nil")
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if !s.configCommitCurrent(commit) {
		return fmt.Errorf("apply config runtime: config commit is stale")
	}
	if ctx == nil {
		return fmt.Errorf("apply config runtime: context is nil")
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	if errManager := s.applyManagerConfig(ctx, commit); errManager != nil {
		return errManager
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if !s.applyPprofConfigContext(ctx, cfg) {
		return fmt.Errorf("apply pprof config")
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if !s.updateServerClientsContext(ctx, cfg) {
		return fmt.Errorf("update server clients")
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}

	registrationCtx := coreauth.WithSkipPersist(ctx)
	if _, errPluginConfig := s.syncPluginRuntimeConfigForConfig(registrationCtx, cfg); errPluginConfig != nil {
		return fmt.Errorf("sync plugin runtime config: %w", errPluginConfig)
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	var auths []*coreauth.Auth
	if s.coreManager != nil {
		auths = s.coreManager.List()
	}
	if errExecutors := s.registerAvailableExecutors(registrationCtx, executorRegistrationOptions{
		includeBaseline:   cfg.Home.Enabled,
		forceReplaceAuths: true,
		auths:             auths,
	}); errExecutors != nil {
		return fmt.Errorf("register executors for config: %w", errExecutors)
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if synthesizeConfigAuths {
		if errRegister := s.registerConfigAPIKeyAuths(registrationCtx, cfg); errRegister != nil {
			return errRegister
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if s.coreManager != nil && !cfg.Home.Enabled && cfg.SaveCooldownStatus {
		if errRestoreCooldown := s.coreManager.RestoreCooldownStates(registrationCtx); errRestoreCooldown != nil {
			return fmt.Errorf("restore cooldown state after config update: %w", errRestoreCooldown)
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if errPluginModels := s.syncPluginModelRuntime(registrationCtx); errPluginModels != nil {
		return fmt.Errorf("sync plugin model runtime: %w", errPluginModels)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func (s *Service) applyManagerConfig(ctx context.Context, commit configCommit) error {
	if s == nil || s.coreManager == nil || commit.cfg == nil {
		if s != nil && commit.cfg != nil {
			return nil
		}
		return fmt.Errorf("apply manager config: service or config is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	routingState := normalizedRoutingRuntimeState(commit.cfg)
	if s.appliedRoutingState == nil || *s.appliedRoutingState != routingState {
		s.coreManager.SetSelector(newRoutingSelector(routingState))
		s.appliedRoutingState = &routingState
	}
	s.applyRetryConfig(commit.cfg)
	store, errStore := s.resolveCooldownStateStore(commit.cfg)
	if errStore != nil {
		return errStore
	}
	if errApply := s.coreManager.ApplyConfigWithCooldownStateStore(ctx, commit.cfg, store); errApply != nil {
		return fmt.Errorf("apply auth manager config: %w", errApply)
	}
	s.coreManager.SetOAuthModelAlias(commit.cfg.OAuthModelAlias)
	return nil
}

func (s *Service) updateServerClientsContext(ctx context.Context, cfg *config.Config) bool {
	if s == nil || cfg == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	if s.updateServerClientsContextFn != nil {
		return s.updateServerClientsContextFn(ctx, cfg)
	}
	if s.server == nil {
		return true
	}
	return s.server.UpdateClientsContext(ctx, cfg)
}

func (s *Service) reloadConfigFromWatcher() bool {
	if s == nil || s.watcher == nil {
		return false
	}
	return s.watcher.ReloadConfigIfChanged()
}

func (s *Service) registerConfigAPIKeyAuths(ctx context.Context, cfg *config.Config) error {
	if s == nil || s.coreManager == nil || cfg == nil {
		return fmt.Errorf("register config API key auths: service, auth manager, and config are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	auths, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		return fmt.Errorf("synthesize config API key auths: %w", errSynthesize)
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(auths))
	needsAliasRebuild := false
	for _, auth := range auths {
		if !coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		prepared, errPrepare := s.prepareCoreAuthForModelRegistration(registrationCtx, auth)
		if errPrepare != nil {
			return errPrepare
		}
		if prepared == nil {
			return fmt.Errorf("prepare config API key auth %s: no auth returned", auth.ID)
		}
		needsAliasRebuild = true
		authForRegistration := prepared
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhaseConfigAPIKey,
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) error {
				return s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
			},
		})
	}
	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	return s.runModelRegistrationTasks(registrationCtx, tasks)
}

func forceHomeRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.APIKeys = nil
	cfg.UsageStatisticsEnabled = true
	cfg.DisableCooling = true
	cfg.SaveCooldownStatus = false
	cfg.WebsocketAuth = false
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Plugins.StoreAuth = nil
}
