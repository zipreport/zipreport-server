package render

import (
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"zipreport-server/pkg/zpt"
)

// Page Sizes
const PageA3 = "A3"
const PageA4 = "A4"
const PageA5 = "A5"
const PageLetter = "Letter"
const PageLegal = "Legal"
const PageTabloid = "Tabloid"

// Margins
const MarginStandard = "standard"
const MarginNone = "none"
const MarginMinimal = "minimal"
const MarginCustom = "custom"

// Defaults
const JobDefaultTimeout = 120
const JobDefaultJsTimeout = 30
const JobDefaultSettlingTime = 200

type Job struct {
	Id                uuid.UUID
	Zpt               *zpt.ZptReader
	IndexFile         string
	PageSize          string
	MarginStyle       string
	MarginLeft        float64 // custom margins
	MarginRight       float64
	MarginTop         float64
	MarginBottom      float64
	Landscape         bool
	JobSettlingTimeMs int
	JobTimeoutS       int
	JsTimeoutS        int
	UseJSEvent        bool
	IgnoreSSLErrors   bool
}

type JobResult struct {
	ElapsedTime float64
	Success     bool
	Output      []byte
	Error       error
}

var ValidPageSizes = []string{PageA3, PageA4, PageA5, PageLetter, PageLegal, PageTabloid}
var ValidMarginStyle = []string{MarginNone, MarginStandard, MarginMinimal, MarginCustom}

func NewRenderJob(z *zpt.ZptReader, id uuid.UUID) *Job {
	return &Job{
		Id:                id,
		Zpt:               z,
		IndexFile:         zpt.DefaultScriptName,
		PageSize:          PageA4,
		MarginStyle:       MarginStandard,
		MarginLeft:        0,
		MarginRight:       0,
		MarginTop:         0,
		MarginBottom:      0,
		Landscape:         false,
		JobSettlingTimeMs: JobDefaultSettlingTime,
		JobTimeoutS:       JobDefaultTimeout,
		JsTimeoutS:        JobDefaultJsTimeout,
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
	case PageLetter:
		return wrap(8.5), wrap(11)
	case PageLegal:
		return wrap(8.5), wrap(14)
	case PageTabloid:
		return wrap(11), wrap(17)
	case PageA5:
		return wrap(5.8), wrap(8.3)
	case PageA3:
		return wrap(11.7), wrap(16.5)
	default: // PageA4
		return wrap(8.3), wrap(11.7)
	}
}

// calcPaperMargin()(top, bottom, left, right
func (r *Job) calcPaperMargin() (*float64, *float64, *float64, *float64) {
	switch r.MarginStyle {
	case MarginMinimal:
		return wrap(0.2), wrap(0.2), wrap(0.2), wrap(0.2)
	case MarginNone:
		return wrap(0), wrap(0), wrap(0), wrap(0)
	case MarginCustom:
		return wrap(r.MarginTop), wrap(r.MarginBottom), wrap(r.MarginLeft), wrap(r.MarginRight)
	// default: MarginStandard
	default:
		return wrap(0.4), wrap(0.4), wrap(0.4), wrap(0.4)
	}
}
func wrap(v float64) *float64 {
	return &v
}
