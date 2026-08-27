package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetModelInventory returns the registry's complete, secret-free routing view.
// Authentication and remote-access policy are enforced by the management group.
func (h *Handler) GetModelInventory(c *gin.Context) {
	c.JSON(http.StatusOK, registry.GetGlobalRegistry().GetModelInventory())
}
