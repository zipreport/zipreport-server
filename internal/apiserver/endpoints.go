package apiserver

import (
	"net/http"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/oddbit-project/blueprint/log"
	httplog "github.com/oddbit-project/blueprint/provider/httpserver/log"
)

func renderAction(g *gin.Context, e *render.Engine, m *monitor.Metrics) {
	// validate requestId
	// Note: reqId may be supplied as an external header, make sure it is not abused
	reqId, err := uuid.Parse(g.GetHeader(httplog.HeaderRequestID))
	if err != nil {
		reqId, _ = uuid.NewRandom()
	}
	logger := log.FromContext(g)

	m.TotalOps.Inc() // update metrics
	job, err := buildRenderJob(g, reqId)
	if err != nil {
		logger.Error(err, "error building render job", log.KV{"reqId": reqId})
		errBadRequest(g, "error building render job")
		return
	}

	result := e.RenderJob(g.Request.Context(), job)
	if !result.Success {
		m.FailedOps.Inc() // update metrics
		logger.Error(result.Error, "error generating pdf", log.KV{"reqId": reqId})
		errServerError(g)
		return
	}
	m.ConversionTime.Observe(result.ElapsedTime)

	// write pdf to output
	g.Writer.Header().Set("Content-Type", "application/pdf")
	_, err = g.Writer.Write(result.Output)
	if err != nil {
		logger.Error(err, "error writing pdf to api response", log.KV{"reqId": reqId})
		m.FailedOps.Inc()
		g.Render(http.StatusInternalServerError, nil)
		return
	} else {
		m.SuccessOps.Inc()
	}
}
