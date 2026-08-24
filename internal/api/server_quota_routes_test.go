package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuotaDowngradeManagementRoutesRequireManagementKey(t *testing.T) {
	const managementKey = "quota-management-key"
	t.Setenv("MANAGEMENT_PASSWORD", managementKey)
	server := newTestServer(t)
	defer server.quotaManager.Stop()

	unauthorized := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v0/management/quota/downgraded", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized list status=%d want=%d", unauthorized.Code, http.StatusUnauthorized)
	}

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/v0/management/quota/downgrade", body: `{"key_hash":"abcdef12","reason":"spend_limit_exceeded","fallback_model":"gpt-5.6-luna","expires_in_seconds":3600}`},
		{method: http.MethodPost, path: "/v0/management/quota/downgrade/resume", body: `{"key_hash":"abcdef12","expected_reason":"spend_limit_exceeded"}`},
		{method: http.MethodGet, path: "/v0/management/quota/downgraded"},
		{method: http.MethodPost, path: "/v0/management/quota/validate-model", body: `{"fallback_model":"gpt-5.6-luna"}`},
	} {
		req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
		req.Header.Set("Authorization", "Bearer "+managementKey)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d want=%d body=%s", request.method, request.path, recorder.Code, http.StatusOK, recorder.Body.String())
		}
	}
}
