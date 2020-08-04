package v1

import (
	"errors"
	"github.com/gin-gonic/gin"
	"mime/multipart"
	"strconv"
	"strings"
	"zipreport-server/pkg/render"
)

const (
	// default script
	SCRIPT_DEFAULT = "report.html"
	REPORT_DEFAULT = "report.pdf"

	// form fields - render
	POST_REPORT              = "report"            // report file
	POST_SCRIPT              = "script"            // main script (str)
	POST_PAGE_SIZE           = "page_size"         // page size (str)
	POST_MARGINS             = "margins"           // margin style (str)
	POST_LANDSCAPE           = "landscape"         // page orientation (bool)
	POST_SETTLING_TIME       = "settling_time"     // job settling time, in milisseconds (int)
	POST_RENDER_TIMEOUT      = "timeout_render"    // render timeout, in seconds(int)
	POST_JS_TIMEOUT          = "timeout_js"        // js event timeout, in seconds (int)
	POST_PROCESS_TIMEOUT     = "timeout_process"   // job process timeout, in seconds (int)
	POST_JS_EVENT            = "js_event"          // usage of js triggered event (bool)
	POST_IGNORE_SSL_ERR      = "ignore_ssl_errors" // ignore ssl errors (bool)
	POST_NO_INSECURE_CONTENT = "secure_only"       // dont allow insecure content (bool)
)

type renderRequest struct {
	Report     *multipart.File
	ReportSize int64
	MainScript string
	JobInfo    render.Job
}

var zipHeader = [4]byte{0x50, 0x4b, 0x03, 0x04,}
var errInvalidFileType = errors.New("Invalid file type")
var errInvalidPageSize = errors.New("Invalid page size")
var errInvalidMargins = errors.New("Invalid margin setting")

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
	return false;
}

func optionalIntValue(ctx *gin.Context, name string, defaultValue int) int {
	if v, exists := ctx.GetPostForm(name); !exists {
		return defaultValue;
	} else {
		// if exists but its not valid int, return default value
		if r, err := strconv.Atoi(v); err != nil {
			return defaultValue;
		} else {
			return r;
		}
	}
}

func optionalBoolValue(ctx *gin.Context, name string, defaultValue bool) bool {
	if v, exists := ctx.GetPostForm(name); !exists {
		return defaultValue;
	} else {
		v := strings.ToLower(v);
		return strExists(v, []string{"true", "1", "t", "y",})
	}
}

/**
 * Assemble renderRequest from Request
 * To simplify implementation of optional fields and validation of specific values,
 * Bind() is not used
 */
func parseRenderRequest(c *gin.Context) (*renderRequest, error) {
	// validate report
	report, rptinfo, err := c.Request.FormFile(POST_REPORT)
	if err != nil {
		return nil, err
	}
	// validate page size
	pagesz := c.Request.PostFormValue(POST_PAGE_SIZE)
	if !strExists(pagesz, []string{render.PAGE_A3, render.PAGE_A4, render.PAGE_A5, render.PAGE_LETTER, render.PAGE_LEGAL, render.PAGE_TABLOID}) {
		return nil, errInvalidPageSize
	}
	// validate margin style
	margins := c.Request.PostFormValue(POST_MARGINS)
	if !strExists(margins, []string{render.MARGIN_NONE, render.MARGIN_STANDARD, render.MARGIN_MINIMAL}) {
		return nil, errInvalidMargins
	}

	// validate main script
	script := c.Request.PostFormValue(POST_SCRIPT)
	if len(script) == 0 {
		script = SCRIPT_DEFAULT
	}

	req := &renderRequest{
		Report:     &report,
		ReportSize: rptinfo.Size,
		MainScript: script,
		JobInfo: render.Job{
			Uri:               "",
			PageSize:          pagesz,
			MarginStyle:       margins,
			Landscape:         optionalBoolValue(c, POST_LANDSCAPE, false),
			JobSettlingTime:   optionalIntValue(c, POST_SETTLING_TIME, render.JobDefaultSettlingTime),
			JobTimeout:        optionalIntValue(c, POST_RENDER_TIMEOUT, render.JobDefaultTimeout),
			UseJSEvent:        optionalBoolValue(c, POST_JS_EVENT, false),
			JSTimeout:         optionalIntValue(c, POST_JS_TIMEOUT, render.JobDefaultJSTimeout),
			NoInsecureContent: optionalBoolValue(c, POST_NO_INSECURE_CONTENT, false),
			IgnoreSSLErrors:   optionalBoolValue(c, POST_IGNORE_SSL_ERR, false),
			ProcessTimeout:    optionalIntValue(c, POST_PROCESS_TIMEOUT, render.JobProcessTimeout),
		},
	}

	return req, nil
}
