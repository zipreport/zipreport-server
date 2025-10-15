package apiserver

import (
	"errors"
	"strconv"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// form fields - render
	ParamReport       = "report"            // report file
	ParamIndexFile    = "script"            // main script (str)
	ParamPageSize     = "page_size"         // page size (str)
	ParamMarginStyle  = "margins"           // margin style (str)
	ParamMarginLeft   = "margin_left"       // left margin in inches (str)
	ParamMarginRight  = "margin_right"      // right margin in inches (str)
	ParamMarginTop    = "margin_top"        // top margin in inches (str)
	ParamMarginBottom = "margin_bottom"     // bottom margin in inches (str)
	ParamLandscape    = "landscape"         // page orientation (bool)
	ParamSettlingTime = "settling_time"     // job settling time, in milliseconds (int)
	ParamJobTimeout   = "timeout_job"       // job timeout, in seconds(int)
	ParamJsTimeout    = "timeout_js"        // js event timeout, in seconds(int)
	ParamJsEvent      = "js_event"          // usage of js triggered event (bool)
	IgnoreSslErr      = "ignore_ssl_errors" // ignore ssl errors (bool)
)

var errInvalidPageSize = errors.New("invalid page size")
var errInvalidMarginStyle = errors.New("invalid margin style")
var errInvalidMarginValue = errors.New("invalid margin value")

/**
 * naive needle in <set>, for small sets
 * Returns true if needle exists in list
 */
func strExists(needle string, list []string) bool {
	for _, b := range list {
		if b == needle {
			return true
		}
	}
	return false
}

func optionalIntValue(ctx *gin.Context, name string, defaultValue int) int {
	if v, exists := ctx.GetPostForm(name); !exists {
		return defaultValue
	} else {
		// if exists but its not valid int, return default value
		if r, err := strconv.Atoi(v); err != nil {
			return defaultValue
		} else {
			return r
		}
	}
}

func optionalBoolValue(ctx *gin.Context, name string, defaultValue bool) bool {
	v, exists := ctx.GetPostForm(name)
	if !exists {
		return defaultValue
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultValue
	}
	return b
}

func strFloatValue(ctx *gin.Context, name string, defaultValue float64) (float64, error) {
	if v, exists := ctx.GetPostForm(name); !exists {
		return defaultValue, nil
	} else {
		return strconv.ParseFloat(v, 64)
	}
}

/**
 * Assemble render.Job() from Request
 * To simplify implementation of optional fields and validation of specific values,
 * Bind() is not used
 */
func buildRenderJob(c *gin.Context, reqId uuid.UUID) (*render.Job, error) {
	// validate zpt stream
	report, rptinfo, err := c.Request.FormFile(ParamReport)
	if err != nil {
		return nil, err
	}
	// build render job
	reader, err := zpt.NewZptReader(report, rptinfo.Size)
	if err != nil {
		return nil, err
	}
	job := render.NewRenderJob(reader, reqId)

	// validate page size
	job.PageSize = c.Request.PostFormValue(ParamPageSize)
	if !strExists(job.PageSize, render.ValidPageSizes) {
		return nil, errInvalidPageSize
	}
	// validate margin style
	job.MarginStyle = c.Request.PostFormValue(ParamMarginStyle)
	if !strExists(job.MarginStyle, render.ValidMarginStyle) {
		return nil, errInvalidMarginStyle
	}
	job.MarginLeft, err = strFloatValue(c, ParamMarginLeft, 0)
	if err != nil || job.MarginLeft < 0 {
		return nil, errInvalidMarginValue
	}

	job.MarginRight, err = strFloatValue(c, ParamMarginRight, 0)
	if err != nil || job.MarginRight < 0 {
		return nil, errInvalidMarginValue
	}
	job.MarginTop, err = strFloatValue(c, ParamMarginTop, 0)
	if err != nil || job.MarginTop < 0 {
		return nil, errInvalidMarginValue
	}
	job.MarginBottom, err = strFloatValue(c, ParamMarginBottom, 0)
	if err != nil || job.MarginBottom < 0 {
		return nil, errInvalidMarginValue
	}

	// validate main script
	job.IndexFile = c.Request.PostFormValue(ParamIndexFile)
	if len(job.IndexFile) == 0 {
		job.IndexFile = zpt.DefaultScriptName
	}

	job.Landscape = optionalBoolValue(c, ParamLandscape, false)
	job.JobSettlingTimeMs = optionalIntValue(c, ParamSettlingTime, render.JobDefaultSettlingTime)
	job.JobTimeoutS = optionalIntValue(c, ParamJobTimeout, render.JobDefaultTimeout)
	job.JsTimeoutS = optionalIntValue(c, ParamJsTimeout, render.JobDefaultJsTimeout)
	job.UseJSEvent = optionalBoolValue(c, ParamJsEvent, false)
	job.IgnoreSSLErrors = optionalBoolValue(c, IgnoreSslErr, false)

	return job, nil
}
