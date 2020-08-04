package render;

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

// Defaults
const JobProcessTimeout = 120
const JobDefaultTimeout = 60
const JobDefaultSettlingTime = 200
const JobDefaultJSTimeout = 8

type Job struct {
	Uri               string
	PageSize          string
	MarginStyle       string
	Landscape         bool
	JobSettlingTime   int
	JobTimeout        int
	UseJSEvent        bool
	JSTimeout         int
	NoInsecureContent bool
	IgnoreSSLErrors   bool
	ProcessTimeout    int
}

type JobResult struct {
	Job         *Job
	ElapsedTime float64
	Success     bool
	Output      string
	Error       error
}

type RenderEngine interface {
	Init() error
    Render(job Job, workdir, dest_file string) (*JobResult, error)
	ValidateJob(job Job) error
}

func NewRenderJob(uri string) Job {
	return Job{
		Uri:               uri,
		PageSize:          PAGE_A4,
		MarginStyle:       MARGIN_STANDARD,
		Landscape:         false,
		JobSettlingTime:   JobDefaultSettlingTime,
		JobTimeout:        JobDefaultTimeout,
		UseJSEvent:        false,
		JSTimeout:         JobDefaultJSTimeout,
		NoInsecureContent: false,
		IgnoreSSLErrors:   true,
		ProcessTimeout:    JobProcessTimeout,
	}
}
