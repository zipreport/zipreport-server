// buildRenderJob is unexported; asserting its parsed values needs package access.
package apiserver

import (
	"bytes"
	"errors"
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
	job, err := tryParseJob(t, fields)
	require.NoError(t, err)
	return job
}

func tryParseJob(t *testing.T, fields map[string]string) (*render.Job, error) {
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
	for k, v := range map[string]string{ParamPageSize: "A4", ParamMarginStyle: "standard"} {
		if _, overridden := fields[k]; !overridden {
			require.NoError(t, mw.WriteField(k, v))
		}
	}
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	require.NoError(t, mw.Close())

	req := httptest.NewRequest(http.MethodPost, "/v2/render", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req

	job, closer, err := buildRenderJob(c, uuid.New())
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return job, err
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

func TestBuildRenderJob_RejectsNonFiniteMargins(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "-Inf"} {
		t.Run(v, func(t *testing.T) {
			_, err := tryParseJob(t, map[string]string{
				ParamMarginStyle: "custom",
				ParamMarginLeft:  v,
			})
			assert.True(t, errors.Is(err, errInvalidMarginValue), "got %v", err)
		})
	}
}

func TestBuildRenderJob_PaperSizeMargins(t *testing.T) {
	cases := []struct {
		name     string
		fields   map[string]string
		accepted bool
	}{
		{"a4_left_right_10", map[string]string{ParamPageSize: "A4", ParamMarginLeft: "5", ParamMarginRight: "5"}, false},
		{"a4_landscape_top_bottom_10", map[string]string{ParamPageSize: "A4", ParamLandscape: "true", ParamMarginTop: "5", ParamMarginBottom: "5"}, false},
		{"a4_half_inch", map[string]string{ParamPageSize: "A4", ParamMarginLeft: "0.5", ParamMarginRight: "0.5", ParamMarginTop: "0.5", ParamMarginBottom: "0.5"}, true},
		{"legal_top_bottom_9", map[string]string{ParamPageSize: "Legal", ParamMarginTop: "4.5", ParamMarginBottom: "4.5"}, true},
		{"a4_landscape_left_right_10", map[string]string{ParamPageSize: "A4", ParamLandscape: "true", ParamMarginLeft: "5", ParamMarginRight: "5"}, true},
		{"a4_boundary_8_3", map[string]string{ParamPageSize: "A4", ParamMarginLeft: "8.3", ParamMarginRight: "0"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]string{ParamMarginStyle: "custom"}
			for k, v := range tc.fields {
				fields[k] = v
			}
			_, err := tryParseJob(t, fields)
			if tc.accepted {
				assert.NoError(t, err)
			} else {
				assert.True(t, errors.Is(err, errInvalidMarginValue), "got %v", err)
			}
		})
	}
}
