package v2

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
)

func POST_Render(ctx *gin.Context, e *render.Engine, l zerolog.Logger, m *monitor.Metrics) {
	reqId := uuid.New()
	job, err := buildRenderJob(ctx, reqId)
	if err != nil {
		l.Error().
			Str("id", reqId.String()).
			Err(err).
			Msg("API error")
		m.IncFailedOps()
		errBadRequest(ctx, err)
		return
	}

	result := e.RenderJob(job)
	if !result.Success {
		// something went wrong
		m.IncFailedOps()
		l.Error().
			Str("id", reqId.String()).
			Err(err).
			Msg("API error generating PDF")
		errServerError(ctx)
		return
	}
	m.ObserveConversionTime(result.ElapsedTime)

	ctx.Writer.Header().Set("Content-Type", "application/pdf")
	_, err = ctx.Writer.Write(result.Output)
	if err != nil {
		l.Warn().
			Str("id", reqId.String()).
			Err(err).
			Msg("API error writing response")
	}
	m.IncSuccessOps()
}
