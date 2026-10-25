// buildRenderJob is unexported; asserting its parsed values needs package access.
package apiserver

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/render"
)

func parseJob(t *testing.T, fields map[string]string) *render.Job {
	t.Helper()
	gin.SetMode(gin.TestMode)

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	f, err := os.Open("../../test/fixtures/test.zpt")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	part, err := mw.CreateFormFile(ParamReport, "test.zpt")
	require.NoError(t, err)
	_, err = io.Copy(part, f)
	require.NoError(t, err)
	require.NoError(t, mw.WriteField(ParamPageSize, "A4"))
	require.NoError(t, mw.WriteField(ParamMarginStyle, "standard"))
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/v2/render", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	job, closer, err := buildRenderJob(c, uuid.New())
	require.NoError(t, err)
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return job
}

func TestBuildRenderJob_ClampsParameters(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		value  string
		get    func(*render.Job) int
		expect int
	}{
		{"timeout_job_max", ParamJobTimeout, "999999", func(j *render.Job) int { return j.JobTimeoutS }, render.JobMaxTimeout},
		{"timeout_job_zero", ParamJobTimeout, "0", func(j *render.Job) int { return j.JobTimeoutS }, 1},
		{"timeout_job_negative", ParamJobTimeout, "-5", func(j *render.Job) int { return j.JobTimeoutS }, 1},
		{"timeout_js_max", ParamJsTimeout, "999999", func(j *render.Job) int { return j.JsTimeoutS }, render.JobMaxJsTimeout},
		{"settling_max", ParamSettlingTime, "999999", func(j *render.Job) int { return j.JobSettlingTimeMs }, render.JobMaxSettlingTime},
		{"settling_negative", ParamSettlingTime, "-1", func(j *render.Job) int { return j.JobSettlingTimeMs }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := parseJob(t, map[string]string{tc.field: tc.value})
			assert.Equal(t, tc.expect, tc.get(job))
		})
	}
}

func TestBuildRenderJob_LandscapeParsing(t *testing.T) {
	cases := []struct {
		value  string
		expect bool
	}{
		{"true", true}, {"1", true}, {"TRUE", true}, {"t", true},
		{"false", false}, {"0", false}, {"invalid", false}, {"yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			job := parseJob(t, map[string]string{ParamLandscape: tc.value})
			assert.Equal(t, tc.expect, job.Landscape)
		})
	}
	t.Run("absent", func(t *testing.T) {
		assert.False(t, parseJob(t, nil).Landscape)
	})
}
