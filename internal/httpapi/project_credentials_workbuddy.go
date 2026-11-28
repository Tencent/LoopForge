package httpapi

import (
	"net/http"

	"agentflow/internal/domain"
	"agentflow/internal/workbuddy"

	"github.com/gin-gonic/gin"
)

func (h *projectCredentialsHandlers) workBuddyModels(c *gin.Context) {
	projectID, _, ok := h.authorize(c, false)
	if !ok {
		return
	}
	config := domain.WorkBuddyConfig{Region: c.Query("region")}
	if err := config.ValidateRegion(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()
	key, err := h.repo.ResolveProjectAKSK(ctx, projectID, c.Param("credentialId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	client, err := workbuddy.New(config, key)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	models, err := client.Models(ctx)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}
