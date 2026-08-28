package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type countingStore struct {
	saveCount atomic.Int32
}

type failingAuthStore struct {
	saveErr   error
	saveCount atomic.Int32
}

func (s *failingAuthStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *failingAuthStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", s.saveErr
}

func (s *failingAuthStore) Delete(context.Context, string) error { return nil }

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}

func TestPersist_SkipsConfigAPIKeyAuth(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "codex:apikey:abc",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "secret",
			"source":  "config:codex[abc]",
		},
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls for config api key, got %d", got)
	}
	if errMark := mgr.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true}); errMark != nil {
		t.Fatalf("MarkResult() error = %v", errMark)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected MarkResult to skip persist for config api key, got %d Save calls", got)
	}
}

func TestManagerRegisterStoreFailureDoesNotPublishAuth(t *testing.T) {
	storeFailure := errors.New("injected auth store save failure")
	store := &failingAuthStore{saveErr: storeFailure}
	manager := NewManager(store, nil, nil)

	_, errRegister := manager.Register(context.Background(), &Auth{
		ID: "register-failure", Provider: "test-provider", Metadata: map[string]any{"token": "secret"},
	})
	if !errors.Is(errRegister, storeFailure) {
		t.Fatalf("Register() error = %v, want injected store failure", errRegister)
	}
	if _, published := manager.GetByID("register-failure"); published {
		t.Fatal("Register() published auth after Store.Save failure")
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("Store.Save calls = %d, want 1", got)
	}
}

func TestManagerUpdateStoreFailurePreservesPublishedAuth(t *testing.T) {
	storeFailure := errors.New("injected auth store save failure")
	store := &failingAuthStore{saveErr: storeFailure}
	manager := NewManager(store, nil, nil)
	ctx := WithSkipPersist(context.Background())
	if _, errRegister := manager.Register(ctx, &Auth{
		ID: "update-failure", Provider: "test-provider", Label: "before", Metadata: map[string]any{"token": "secret"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	_, errUpdate := manager.Update(context.Background(), &Auth{
		ID: "update-failure", Provider: "test-provider", Label: "after", Metadata: map[string]any{"token": "secret"},
	})
	if !errors.Is(errUpdate, storeFailure) {
		t.Fatalf("Update() error = %v, want injected store failure", errUpdate)
	}
	published, ok := manager.GetByID("update-failure")
	if !ok || published == nil {
		t.Fatal("existing auth disappeared after failed update")
	}
	if published.Label != "before" {
		t.Fatalf("published auth label = %q, want pre-update value", published.Label)
	}
}

func TestManagerMarkResultStoreFailurePreservesPublishedState(t *testing.T) {
	storeFailure := errors.New("injected auth store save failure")
	store := &failingAuthStore{saveErr: storeFailure}
	manager := NewManager(store, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID: "result-failure", Provider: "test-provider", Metadata: map[string]any{"token": "secret"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	errMark := manager.MarkResult(context.Background(), Result{
		AuthID: "result-failure", Provider: "test-provider", Model: "test-model", Success: true,
	})
	if !errors.Is(errMark, storeFailure) {
		t.Fatalf("MarkResult() error = %v, want injected store failure", errMark)
	}
	published, ok := manager.GetByID("result-failure")
	if !ok || published == nil {
		t.Fatal("auth disappeared after failed result persistence")
	}
	if published.Success != 0 || published.Failed != 0 || len(published.ModelStates) != 0 {
		t.Fatalf("result state published after Store.Save failure: success=%d failed=%d states=%d", published.Success, published.Failed, len(published.ModelStates))
	}
}
