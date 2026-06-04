package apiserver

import (
	"crypto/subtle"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"

	"github.com/gin-gonic/gin"
	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/oddbit-project/blueprint/provider/httpserver/auth"
)

const (
	DefaultReadTimeout  = 300
	DefaultWriteTimeout = 300
	DefaultPort         = 6543
)

// ApiServerConfig holds the HTTP server settings plus token-auth and
// security-header options. The httpserver provider no longer carries these in
// an Options map, so they are configured here and applied in NewApiServer.
type ApiServerConfig struct {
	httpserver.ServerConfig
	AuthTokenHeader        string `json:"authTokenHeader"`
	AuthTokenSecret        string `json:"authTokenSecret"`
	DefaultSecurityHeaders bool   `json:"defaultSecurityHeaders"`
}

// constantTimeToken is an auth.Provider that compares the token header against
// the configured secret in constant time, avoiding the timing side-channel of
// the framework's default string comparison.
type constantTimeToken struct {
	header string
	key    string
}

func (a constantTimeToken) CanAccess(c *gin.Context) bool {
	if len(a.key) == 0 {
		return false
	}
	got := c.Request.Header.Get(a.header)
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.key)) == 1
}

func NewApiServer(cfg *ApiServerConfig, engine *render.Engine, metrics *monitor.Metrics, logger *log.Logger) (*httpserver.Server, error) {
	srv, err := httpserver.NewServer(&cfg.ServerConfig, logger)
	if err != nil {
		return nil, err
	}

	if cfg.DefaultSecurityHeaders {
		srv.UseDefaultSecurityHeaders()
	}

	header := cfg.AuthTokenHeader
	if header == "" {
		header = auth.DefaultTokenAuthHeader
	}
	if cfg.AuthTokenSecret != "" {
		srv.UseAuth(constantTimeToken{header: header, key: cfg.AuthTokenSecret})
	}

	v := srv.Group("v2")
	{
		v.POST("/render", func(g *gin.Context) {
			renderAction(g, engine, metrics)
		})
	}

	return srv, nil
}
