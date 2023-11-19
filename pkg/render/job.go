package render

import (
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"zipreport-server/pkg/zpt"
)

// Page Sizes
const PAGE_A3 = "A3"
const PAGE_A4 = "A4"
const PAGE_A5 = "A5"
const PAGE_LETTER = "Letter"
const PAGE_LEGAL = "Legal"
const PAGE_TABLOID = "Tabloid"

// Margins
const MARGIN_STANDARD = "standard"
const MARGIN_NONE = "none"
const MARGIN_MINIMAL = "minimal"
const MARGIN_CUSTOM = "custom"

// Defaults
const JobDefaultTimeout = 120
const JobDefaultSettlingTime = 30

type Job struct {
	Id                uuid.UUID
	Zpt               *zpt.ZptReader
	PageSize          string
	MarginStyle       string
	MarginLeft        float64 // custom margins
	MarginRight       float64
	MarginTop         float64
	MarginBottom      float64
	Landscape         bool
	JobSettlingTimeMs int
	JobTimeoutS       int
	UseJSEvent        bool
	IgnoreSSLErrors   bool
}

type JobResult struct {
	ElapsedTime float64
	Success     bool
	Output      []byte
	Error       error
}

func NewRenderJob(z *zpt.ZptReader) *Job {
	return &Job{
		Id:                uuid.New(),
		Zpt:               z,
		PageSize:          PAGE_A4,
		MarginStyle:       MARGIN_STANDARD,
		Landscape:         false,
		JobSettlingTimeMs: JobDefaultSettlingTime,
		JobTimeoutS:       JobDefaultTimeout,
		UseJSEvent:        false,
		IgnoreSSLErrors:   true,
	}
}

func (r *Job) ToPDFOptions() *proto.PagePrintToPDF {
	top, bottom, left, right := r.calcPaperMargin()
	width, height := r.calcPaperSize()
	return &proto.PagePrintToPDF{
		Landscape:           r.Landscape,
		DisplayHeaderFooter: false,
		PrintBackground:     false,
		Scale:               wrap(1.0),
		PaperWidth:          width,
		PaperHeight:         height,
		MarginTop:           top,
		MarginBottom:        bottom,
		MarginLeft:          left,
		MarginRight:         right,
		PageRanges:          "",
		HeaderTemplate:      "",
		FooterTemplate:      "",
		PreferCSSPageSize:   false,
		TransferMode:        proto.PagePrintToPDFTransferModeReturnAsStream,
	}
}

// calcPaperSize()(width, height)
func (r *Job) calcPaperSize() (*float64, *float64) {
	switch r.PageSize {
	case PAGE_LETTER:
		return wrap(8.5), wrap(11)
	case PAGE_LEGAL:
		return wrap(8.5), wrap(14)
	case PAGE_TABLOID:
		return wrap(11), wrap(17)
	case PAGE_A5:
		return wrap(5.8), wrap(8.3)
	case PAGE_A3:
		return wrap(11.7), wrap(16.5)
	default: // PAGE_A4
		return wrap(8.3), wrap(11.7)
	}
}

// calcPaperMargin()(top, bottom, left, right
func (r *Job) calcPaperMargin() (*float64, *float64, *float64, *float64) {
	switch r.MarginStyle {
	case MARGIN_MINIMAL:
		return wrap(0.2), wrap(0.2), wrap(0.2), wrap(0.2)
	case MARGIN_NONE:
		return wrap(0), wrap(0), wrap(0), wrap(0)
	case MARGIN_CUSTOM:
		return wrap(r.MarginTop), wrap(r.MarginBottom), wrap(r.MarginLeft), wrap(r.MarginRight)
	// default: MARGIN_STANDARD
	default:
		return wrap(0.4), wrap(0.4), wrap(0.4), wrap(0.4)
	}
}
func wrap(v float64) *float64 {
	return &v
}
