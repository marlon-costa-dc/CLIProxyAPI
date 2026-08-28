package auth

import (
	"context"
	"testing"
)

func mustMarkResult(tb testing.TB, manager *Manager, ctx context.Context, result Result) {
	tb.Helper()
	if errMark := manager.MarkResult(ctx, result); errMark != nil {
		tb.Fatalf("MarkResult() error = %v", errMark)
	}
}

func mustRegisterAuth(tb testing.TB, manager *Manager, ctx context.Context, auth *Auth) {
	tb.Helper()
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		tb.Fatalf("Register() error = %v", errRegister)
	}
}
