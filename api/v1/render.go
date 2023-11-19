package v1

import (
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	"path/filepath"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"
)

func POST_Render(ctx *gin.Context, s zpt.Backend, r render.RenderEngine, m *monitor.Metrics) {
	req, err := parseRenderRequest(ctx)
	if err != nil {
		m.IncFailed()
		errBadRequest(ctx, err)
		return
	}

	tmpfolder, err := s.UnpackZipFromReader(*req.Report, req.ReportSize)
	if err != nil {
		m.IncFailed()
		errBadRequest(ctx, err)
		return
	}
	working_dir, err := s.GetPath(tmpfolder)
	if err != nil {
		m.IncFailed()
		errBadRequest(ctx, err)
		return
	}

	report := filepath.Join(working_dir, REPORT_DEFAULT)
	req.JobInfo.Uri = filepath.Join(working_dir, "report.html")
	result, err := r.Render(req.JobInfo, working_dir, REPORT_DEFAULT)
	logRenderJob(result, working_dir, report)

	if err != nil {
		m.IncFailed()
		errBadRequest(ctx, err)
		s.RemoveTmpFolder(tmpfolder)
		return
	}

	m.ObserveConversionTime(result.ElapsedTime)
	pdf, err := os.Open(report)
	if err != nil {
		m.IncFailed()
		s.RemoveTmpFolder(tmpfolder)
		log.Warn("Unexpected error reading generated report: " + report)
		errServerError(ctx)
		return
	}

	defer pdf.Close()
	ctx.Writer.Header().Set("Content-Type", "application/pdf")
	io.Copy(ctx.Writer, pdf)
	s.RemoveTmpFolder(tmpfolder)
	m.IncSuccess()
}

func logRenderJob(jobResult *render.JobResult, workDir, report string) {
	var msg string
	if jobResult.Success {
		msg = "PDF conversion successful"
	} else {
		msg = "PDF conversion failed"
	}
	log.WithFields(log.Fields{
		"workingDir":      workDir,
		"uri":             jobResult.Job.Uri,
		"generatedReport": report,
		"success":         jobResult.Success,
		"elapsedTime":     jobResult.ElapsedTime,
		"consoleOutput":   jobResult.Output,
		"error":           jobResult.Error,
	}).Info(msg)
}
