package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/cnb"
	"agentflow/internal/domain"

	"github.com/gin-gonic/gin"
)

// cnbHandlers manages the current user's personal CNB (cnb.cool) access token.
// The token needs repo-code:rw and repo-pr:rw and is only sent to the CNB API.
type cnbHandlers struct {
	*deps
}

// cnbAPIBase overrides the CNB API address; tests point it at a local server.
var cnbAPIBase = ""

func newCNBClient(token string) *cnb.Client {
	client := cnb.New(token)
	if cnbAPIBase != "" {
		client.BaseURL = cnbAPIBase
	}
	return client
}

// ownUserID is the :id of the route when it is the signed-in user.
func (h *cnbHandlers) ownUserID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid user id is required"})
		return 0, false
	}
	if user, ok := h.requestUser(c.Request, false); !ok || user.ID != id {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能管理当前登录用户的 CNB 令牌"})
		return 0, false
	}
	return id, true
}

// 路由: g.PUT("/api/users/:id/cnb-token", api.cnb.saveToken)
func (h *cnbHandlers) saveToken(c *gin.Context) {
	id, ok := h.ownUserID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var input struct {
		Token string `json:"token"`
	}
	if !decode(c, &input) {
		return
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请粘贴 CNB 访问令牌"})
		return
	}
	h.storeToken(c, id, token)
}

// 路由: g.DELETE("/api/users/:id/cnb-token", api.cnb.clearToken)
func (h *cnbHandlers) clearToken(c *gin.Context) {
	if id, ok := h.ownUserID(c); ok {
		h.storeToken(c, id, "")
	}
}

func (h *cnbHandlers) storeToken(c *gin.Context, id int64, token string) {
	if err := h.repo.SetCNBToken(id, token); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	user, _ := h.repo.User(id)
	c.JSON(http.StatusOK, domain.PublicUser(user))
}

// 路由: g.GET/POST("/api/users/:id/cnb-token/verify", api.cnb.verifyToken)
// GET checks the saved token; POST checks body.token before it is saved.
// The answer is always HTTP 200 with a structured result.
func (h *cnbHandlers) verifyToken(c *gin.Context) {
	id, ok := h.ownUserID(c)
	if !ok {
		return
	}
	token := ""
	if c.Request.Method == http.MethodPost {
		var input struct {
			Token string `json:"token"`
		}
		if !decode(c, &input) {
			return
		}
		token = strings.TrimSpace(input.Token)
	}
	if token == "" {
		user, _ := h.repo.User(id)
		token = user.CNBToken
	}
	if token == "" {
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "尚未配置 CNB 访问令牌，请先在个人配置中填写"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	login, err := newCNBClient(token).VerifyToken(ctx)
	var apiErr *cnb.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "CNB 访问令牌无效或已过期"})
	case err != nil:
		c.JSON(http.StatusOK, gitTokenVerifyResult{Error: "无法连接 CNB，请稍后重试"})
	default:
		// A token limited to repo-code and repo-pr cannot read /user: valid, login unknown.
		c.JSON(http.StatusOK, gitTokenVerifyResult{Valid: true, Login: login})
	}
}
