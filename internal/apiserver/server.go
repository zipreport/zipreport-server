package apiserver

import (
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"

	"github.com/gin-gonic/gin"
	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
)

const (
	DefaultReadTimeout  = 300
	DefaultWriteTimeout = 300
	DefaultPort         = 6543
)

func NewApiServer(cfg *httpserver.ServerConfig, engine *render.Engine, metrics *monitor.Metrics, logger *log.Logger) (*httpserver.Server, error) {
	srv, err := cfg.NewServer(logger)
	if err != nil {
		return nil, err
	}
	// enable security headers and auth
	if err = srv.ProcessOptions(); err != nil {
		return nil, err
	}

	v := srv.Router.Group("v2")
	{
		v.POST("/render", func(g *gin.Context) {
			renderAction(g, engine, metrics)
		})
	}

	return srv, nil
}
