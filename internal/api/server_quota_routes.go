package api

import "github.com/gin-gonic/gin"

// registerQuotaManagementRoutes registers the local API key pause/resume
// management routes. Kept in a dedicated file so upstream merges that touch
// server_management.go do not conflict with these local-only routes.
func (s *Server) registerQuotaManagementRoutes(mgmt *gin.RouterGroup) {
	mgmt.POST("/quota/pause", s.mgmt.PostPauseKey)
	mgmt.POST("/quota/resume", s.mgmt.PostResumeKey)
	mgmt.GET("/quota/paused", s.mgmt.GetPausedKeys)
	mgmt.GET("/quota/config", s.mgmt.GetQuotaConfig)
	mgmt.PUT("/quota/config", s.mgmt.PutQuotaConfig)
}
