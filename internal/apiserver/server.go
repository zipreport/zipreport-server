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

func NewApiServer(cfg *httpserver.ServerConfig, engine *render.Engine, metrics *monitor.Metrics, logger *log.Logger) (*httpserver.Server, error) {
	srv, err := cfg.NewServer(logger)
	if err != nil {
		return nil, err
	}

	// Capture the token config and remove the secret so ProcessOptions does not
	// install the framework's non-constant-time token auth; we register our own.
	header := cfg.Options[httpserver.OptAuthTokenHeader]
	if header == "" {
		header = auth.DefaultTokenAuthHeader
	}
	secret := cfg.Options[httpserver.OptAuthTokenSecret]
	delete(cfg.Options, httpserver.OptAuthTokenSecret)

	// enable security headers
	if err = srv.ProcessOptions(); err != nil {
		return nil, err
	}
	if secret != "" {
		srv.UseAuth(constantTimeToken{header: header, key: secret})
	}

	v := srv.Router.Group("v2")
	{
		v.POST("/render", func(g *gin.Context) {
			renderAction(g, engine, metrics)
		})
	}

	return srv, nil
}
