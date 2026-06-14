package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

func TestGetPausedKeysReturnsEmptyListWhenQuotaDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota/paused", nil)

	h.GetPausedKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Enabled bool              `json:"enabled"`
		Entries []quota.PauseEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Enabled {
		t.Fatal("enabled = true, want false")
	}
	if len(body.Entries) != 0 {
		t.Fatalf("entries len = %d, want 0", len(body.Entries))
	}
}

func TestGetQuotaConfigReturnsCurrentConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		Quota: quota.QuotaConfig{
			Enabled: true,
			DBPath:  "quota.db",
			Default: quota.SpendLimit{DailyCents: 100, WeeklyCents: 700},
			Overrides: []quota.SpendLimitEntry{{
				ApplyTo:     "api-key",
				ApplyValue:  "abc",
				DailyCents:  200,
				WeeklyCents: 900,
			}},
		},
	}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota/config", nil)

	h.GetQuotaConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Enabled   bool `json:"enabled"`
		DBPath    string `json:"db_path"`
		Default   struct {
			DailyCents  int64 `json:"daily_cents"`
			WeeklyCents int64 `json:"weekly_cents"`
		} `json:"default"`
		Overrides []struct {
			ApplyTo     string `json:"apply_to"`
			ApplyValue  string `json:"apply_value"`
			DailyCents  int64 `json:"daily_cents"`
			WeeklyCents int64 `json:"weekly_cents"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.Enabled || body.DBPath != "quota.db" {
		t.Fatalf("unexpected top-level config: %+v", body)
	}
	if body.Default.DailyCents != 100 || body.Default.WeeklyCents != 700 {
		t.Fatalf("unexpected default config: %+v", body.Default)
	}
	if len(body.Overrides) != 1 {
		t.Fatalf("overrides len = %d, want 1", len(body.Overrides))
	}
	if body.Overrides[0].ApplyTo != "api-key" || body.Overrides[0].ApplyValue != "abc" {
		t.Fatalf("unexpected override: %+v", body.Overrides[0])
	}
}
