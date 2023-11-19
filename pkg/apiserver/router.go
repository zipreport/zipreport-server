package apiserver

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/toorop/gin-logrus"
	"zipreport-server/api/v1"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"
)

func DefaultRouter(cfg *Configuration, s zpt.Backend, r render.RenderEngine, m *monitor.Metrics) *gin.Engine {

	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(ginlogrus.Logger(log.New()))
	router.Use(gin.Recovery())
	router.Use(AuthMiddleware(cfg.ApiKey))

	if m != nil {
		router.GET("/metrics", func(ctx *gin.Context) {
			promhttp.Handler().ServeHTTP(ctx.Writer, ctx.Request)
		})
	}
	v := router.Group("v1")
	{
		v.POST("/render", func(ctx *gin.Context) {
			v1.POST_Render(ctx, s, r, m)
		})
	}
	return router
}
