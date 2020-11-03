package render

import (
	"context"
	"errors"
	log "github.com/sirupsen/logrus"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Error codes
var ErrBinaryNotFound = errors.New("zpt-cli binary not found")
var ErrBinaryNotValid = errors.New("zpt-cli is either not a binary or not executable")
var ErrJobInvalidPageSize = errors.New("Invalid page size")
var ErrJobInvalidMargin = errors.New("Invalid margin configuration")
var ErrJobInvalidValues = errors.New("Invalid job values")

type ZptRenderer struct {
	RenderEngine
	cliBinary string
	noSandbox bool
}

func NewZptRenderer(binary_path string, noSandbox bool) *ZptRenderer {
	return &ZptRenderer{
		cliBinary: binary_path,
		noSandbox: noSandbox,
	}
}

func (z *ZptRenderer) Init() error {
	if info, err := os.Stat(z.cliBinary); os.IsNotExist(err) {
		return ErrBinaryNotFound
	} else {
		if info.IsDir() || !(info.Mode()&0111 != 0) {
			return ErrBinaryNotValid
		}
	}
	return nil
}

func (z *ZptRenderer) Render(job Job, workdir, dest_file string) (*JobResult, error) {
	if err := z.ValidateJob(job); err != nil {
		return nil, err
	}
	// build argument list
	args := append([]string{job.Uri, dest_file}, z.assembleOptions(job)...)

	// use workdir if specified
	if len(workdir) > 0 {
		if err := os.Chdir(workdir); err != nil {
			return nil, err
		}
	}

	result := &JobResult{
		Job:         &job,
		ElapsedTime: 0,
		Success:     false,
		Output:      "",
		Error:       nil,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.ProcessTimeout)*time.Second)
	defer cancel()
	startTime := time.Now()
	cmd := exec.CommandContext(ctx, z.cliBinary, args...)
	buf, err := cmd.CombinedOutput()

	result.ElapsedTime = time.Now().Sub(startTime).Seconds()
	result.Output = string(buf)

	log.WithFields(log.Fields{
		"cliBinary":   z.cliBinary,
		"params":      args,
		"output":      result.Output,
		"error":       err,
		"elapsedTime": result.ElapsedTime,
	}).Info("Executed zpt-cli")

	if err := ctx.Err(); err != nil {
		result.Error = err;
		return result, err;
	} else {
		result.Success = true;
	}
	return result, nil;
}

func (z *ZptRenderer) ValidateJob(job Job) error {
	switch job.PageSize {
	case PAGE_A3, PAGE_A4, PAGE_A5, PAGE_LETTER, PAGE_LEGAL, PAGE_TABLOID:
		break;
	default:
		return ErrJobInvalidPageSize
	}

	switch (job.MarginStyle) {
	case MARGIN_MINIMAL, MARGIN_NONE, MARGIN_STANDARD:
		break;
	default:
		return ErrJobInvalidMargin
	}

	if job.JobSettlingTime < 0 || job.JobTimeout < 0 || job.JSTimeout < 0 || job.ProcessTimeout < 0 {
		return ErrJobInvalidValues
	}
	return nil
}

func (z *ZptRenderer) assembleOptions(job Job) []string {
	opts := []string{
		"--timeout=" + strconv.Itoa(job.JobTimeout),
		"--delay=" + strconv.Itoa(job.JobSettlingTime),
		"--pagesize=" + job.PageSize,
		"--margins=" + job.MarginStyle,
	}

	if (z.noSandbox) {
		opts = append(opts, "--no-sandbox")
	}
	if job.Landscape {
		opts = append(opts, "--no-portrait")
	}
	if job.NoInsecureContent {
		opts = append(opts, "--no-insecure")
	}
	if job.UseJSEvent {
		opts = append(opts,
			"--js-event",
			"--js-timeout="+strconv.Itoa(job.JSTimeout),
		);
	}
	return opts
}
