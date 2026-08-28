package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	modelRegistrationMaxWorkersPerCategory         = 5
	modelRegistrationMaxWorkersOpenAICompatibility = 20
	homeSubscriberPreAckRetryBackoff               = 100 * time.Millisecond
)

const (
	modelRegistrationPhaseConfigAPIKey = iota
	modelRegistrationPhaseOther
)

type modelRegistrationTask struct {
	phase    int
	category string
	run      func(*openAICompatibilityRegistrationCache) error
}

type executorRegistrationOptions struct {
	includeBaseline   bool
	includePlugins    bool
	forceReplaceAuths bool
	auths             []*coreauth.Auth
}

var registerPluginExecutors = func(host *pluginhost.Host, manager *coreauth.Manager) {
	if host == nil || manager == nil {
		return
	}
	host.RegisterExecutors(manager, registry.GetGlobalRegistry())
}

// RegisterUsagePlugin registers a usage plugin on the global usage manager.
// This allows external code to monitor API usage and token consumption.
//
// Parameters:
//   - plugin: The usage plugin to register
func (s *Service) RegisterUsagePlugin(plugin usage.Plugin) {
	usage.RegisterPlugin(plugin)
}

func (s *Service) registerPluginAuthParser() {
	var parser PluginAuthParser
	if s != nil && s.pluginHost != nil {
		parser = s.pluginHost
	}
	sdkAuth.RegisterPluginAuthParser(parser)
	if s != nil && s.watcher != nil {
		s.watcher.SetPluginAuthParser(parser)
	}
}

func (s *Service) syncPluginRuntime(ctx context.Context) error {
	configured, errConfig := s.syncPluginRuntimeConfig(ctx)
	if errConfig != nil {
		return errConfig
	}
	if !configured {
		return nil
	}
	return s.syncPluginModelRuntime(ctx)
}

func (s *Service) syncPluginRuntimeConfig(ctx context.Context) (bool, error) {
	if s == nil {
		sdkAuth.RegisterPluginAuthParser(nil)
		return false, nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return s.syncPluginRuntimeConfigForConfig(ctx, cfg)
}

func (s *Service) syncPluginRuntimeConfigForConfig(ctx context.Context, cfg *config.Config) (bool, error) {
	if s == nil {
		sdkAuth.RegisterPluginAuthParser(nil)
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false, errContext
	}

	if s.pluginHost != nil {
		s.pluginHost.ApplyConfig(ctx, cfg)
	}
	if errContext := ctx.Err(); errContext != nil {
		return false, errContext
	}
	if s.coreManager != nil {
		s.coreManager.SetPluginScheduler(s.pluginHost)
	}
	s.registerPluginAuthParser()
	if s.pluginHost == nil {
		return false, nil
	}
	s.pluginHost.RegisterFrontendAuthProviders()
	if errContext := ctx.Err(); errContext != nil {
		return false, errContext
	}
	if s.accessManager != nil {
		s.accessManager.SetProviders(sdkaccess.RegisteredProviders())
	}
	s.pluginHost.RegisterUsagePlugins()
	sdktranslator.SetPluginHooks(s.pluginHost)
	if s.server != nil {
		s.server.RefreshPluginManagementRoutes()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false, errContext
	}
	return true, nil
}

func (s *Service) syncPluginModelRuntime(ctx context.Context) error {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.pluginHost.RegisterModels(ctx, registry.GetGlobalRegistry())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.cfgMu.RLock()
	homeEnabled := s.cfg != nil && s.cfg.Home.Enabled
	s.cfgMu.RUnlock()
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{
		includeBaseline:   homeEnabled,
		includePlugins:    true,
		forceReplaceAuths: false,
		auths:             s.coreManager.List(),
	})
	if errRefresh := s.refreshPluginModelRegistrations(ctx); errRefresh != nil {
		return errRefresh
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.coreManager.RefreshSchedulerAll()
	return nil
}

func (s *Service) refreshPluginModelRegistrations(ctx context.Context) error {
	if s == nil || s.pluginHost == nil || s.coreManager == nil {
		return nil
	}
	return s.registerModelsForAuthBatch(ctx, s.coreManager.List())
}

func (s *Service) registerModelsForAuthBatch(ctx context.Context, auths []*coreauth.Auth) error {
	if s == nil || s.coreManager == nil || len(auths) == 0 {
		return nil
	}
	tasks := make([]modelRegistrationTask, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		authForRegistration := auth.Clone()
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhase(authForRegistration),
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) error {
				return s.completeModelRegistrationForAuthWithCache(ctx, authForRegistration, compatCache)
			},
		})
	}
	return s.runModelRegistrationTasks(ctx, tasks)
}

func (s *Service) runModelRegistrationTasks(ctx context.Context, tasks []modelRegistrationTask) error {
	if len(tasks) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	configAPIKeyTasks := make([]modelRegistrationTask, 0)
	otherTasks := make([]modelRegistrationTask, 0)
	for _, task := range tasks {
		if task.phase == modelRegistrationPhaseConfigAPIKey {
			configAPIKeyTasks = append(configAPIKeyTasks, task)
			continue
		}
		otherTasks = append(otherTasks, task)
	}

	compatCache := s.newOpenAICompatibilityRegistrationCache()
	if errPhase := s.runModelRegistrationTaskPhase(ctx, configAPIKeyTasks, compatCache); errPhase != nil {
		return fmt.Errorf("register config API key models: %w", errPhase)
	}
	if errPhase := s.runModelRegistrationTaskPhase(ctx, otherTasks, compatCache); errPhase != nil {
		return fmt.Errorf("register runtime auth models: %w", errPhase)
	}
	return nil
}

func (s *Service) runModelRegistrationTaskPhase(ctx context.Context, tasks []modelRegistrationTask, compatCache *openAICompatibilityRegistrationCache) error {
	if len(tasks) == 0 {
		return nil
	}

	grouped := make(map[string][]modelRegistrationTask)
	order := make([]string, 0)
	for _, task := range tasks {
		if task.run == nil {
			continue
		}
		category := strings.ToLower(strings.TrimSpace(task.category))
		if category == "" {
			category = "unknown"
		}
		if _, exists := grouped[category]; !exists {
			order = append(order, category)
		}
		grouped[category] = append(grouped[category], task)
	}

	var wg sync.WaitGroup
	phaseErrors := make([]error, len(order))
	phaseErrorMu := make([]sync.Mutex, len(order))
	for categoryIndex, category := range order {
		group := grouped[category]
		workers := len(group)
		maxWorkers := modelRegistrationMaxWorkersForCategory(category)
		if workers > maxWorkers {
			workers = maxWorkers
		}
		if workers <= 0 {
			continue
		}

		taskCh := make(chan modelRegistrationTask)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for task := range taskCh {
					select {
					case <-ctx.Done():
						return
					default:
					}
					if errRun := task.run(compatCache); errRun != nil {
						phaseErrorMu[categoryIndex].Lock()
						phaseErrors[categoryIndex] = errors.Join(phaseErrors[categoryIndex], errRun)
						phaseErrorMu[categoryIndex].Unlock()
					}
				}
			}()
		}
		go func(group []modelRegistrationTask) {
			defer close(taskCh)
			for _, task := range group {
				select {
				case <-ctx.Done():
					return
				case taskCh <- task:
				}
			}
		}(group)
	}
	wg.Wait()
	if errContext := ctx.Err(); errContext != nil {
		return errors.Join(errContext, errors.Join(phaseErrors...))
	}
	return errors.Join(phaseErrors...)
}

func modelRegistrationPhase(auth *coreauth.Auth) int {
	if coreauth.IsConfigAPIKeyAuth(auth) {
		return modelRegistrationPhaseConfigAPIKey
	}
	return modelRegistrationPhaseOther
}

func modelRegistrationCategory(auth *coreauth.Auth) string {
	if auth == nil {
		return "unknown"
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if compatProviderKey, _, compatDetected := openAICompatInfoFromAuth(auth); compatDetected {
		if compatProviderKey != "" {
			provider = compatProviderKey
		} else {
			provider = "openai-compatibility"
		}
	}
	if provider == "" {
		provider = "unknown"
	}

	authKind := auth.AuthKind()
	if authKind == "" {
		return provider
	}
	return provider + ":" + authKind
}

func modelRegistrationMaxWorkersForCategory(category string) int {
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.HasPrefix(category, "openai-compatible-") || strings.HasPrefix(category, "openai-compatibility") {
		return modelRegistrationMaxWorkersOpenAICompatibility
	}
	return modelRegistrationMaxWorkersPerCategory
}

func (s *Service) registerModelRefreshCallback() {
	// Register callback for startup and periodic model catalog refresh.
	// When remote model definitions change, re-register models for affected providers.
	// This intentionally rebuilds per-auth model availability from the latest catalog
	// snapshot instead of preserving prior registry suppression state.
	registry.SetModelRefreshCallback(func(changedProviders []string) {
		if s == nil || s.coreManager == nil || len(changedProviders) == 0 {
			return
		}

		providerSet := make(map[string]bool, len(changedProviders))
		for _, p := range changedProviders {
			providerSet[strings.ToLower(strings.TrimSpace(p))] = true
		}

		auths := s.coreManager.List()
		refreshed := 0
		var refreshedMu sync.Mutex
		tasks := make([]modelRegistrationTask, 0, len(auths))
		for _, item := range auths {
			if item == nil || item.ID == "" {
				continue
			}
			auth, ok := s.coreManager.GetByID(item.ID)
			if !ok || auth == nil || auth.Disabled {
				continue
			}
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if !providerSet[provider] {
				continue
			}
			authForRefresh := auth
			tasks = append(tasks, modelRegistrationTask{
				phase:    modelRegistrationPhase(authForRefresh),
				category: modelRegistrationCategory(authForRefresh),
				run: func(compatCache *openAICompatibilityRegistrationCache) error {
					refreshedAuth, errRefresh := s.refreshModelRegistrationForAuthWithCache(authForRefresh, compatCache)
					if errRefresh != nil {
						return errRefresh
					}
					if refreshedAuth {
						refreshedMu.Lock()
						refreshed++
						refreshedMu.Unlock()
					}
					return nil
				},
			})
		}
		if errRegister := s.runModelRegistrationTasks(context.Background(), tasks); errRegister != nil {
			s.reportRuntimeError(fmt.Errorf("refresh model catalog registrations: %w", errRegister))
			return
		}

		if refreshed > 0 {
			log.Infof("re-registered models for %d auth(s) due to model catalog changes: %v", refreshed, changedProviders)
		}
	})
}
