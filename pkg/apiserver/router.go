package apiserver

import (
	"github.com/gin-contrib/logger"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"zipreport-server/pkg/apiserver/api/v2"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
)

func ApiRouter(cfg *ApiOptions, e *render.Engine, m *monitor.Metrics) *gin.Engine {

	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(logger.SetLogger())
	router.Use(gin.Recovery())
	router.Use(AuthMiddleware(cfg.ApiKey))

	if cfg.UseMetrics {
		router.GET("/metrics", func(ctx *gin.Context) {
			promhttp.Handler().ServeHTTP(ctx.Writer, ctx.Request)
		})
	}
	v := router.Group("v2")
	{
		v.POST("/render", func(ctx *gin.Context) {
			v2.POST_Render(ctx, e, cfg.Log, m)
		})
	}
	return router
}
