package api

import "github.com/gin-gonic/gin"

// registerQuotaManagementRoutes registers local API-key quota management routes.
// Kept in a dedicated file so upstream merges that touch server_management.go
// do not conflict with these local-only routes.
func (s *Server) registerQuotaManagementRoutes(mgmt *gin.RouterGroup) {
	mgmt.POST("/quota/pause", s.mgmt.PostPauseKey)
	mgmt.POST("/quota/resume", s.mgmt.PostResumeKey)
	mgmt.GET("/quota/paused", s.mgmt.GetPausedKeys)
	mgmt.POST("/quota/downgrade", s.mgmt.PostDowngradeKey)
	mgmt.POST("/quota/downgrade/resume", s.mgmt.PostResumeDowngradeKey)
	mgmt.GET("/quota/downgraded", s.mgmt.GetDowngradedKeys)
	mgmt.POST("/quota/validate-model", s.mgmt.PostValidateFallbackModel)
	mgmt.GET("/quota/config", s.mgmt.GetQuotaConfig)
	mgmt.PUT("/quota/config", s.mgmt.PutQuotaConfig)
}
