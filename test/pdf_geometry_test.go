package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/render"
)

func TestToPDFOptions_PageSizes(t *testing.T) {
	cases := []struct {
		size string
		w, h float64
	}{
		{render.PageLetter, 8.5, 11},
		{render.PageLegal, 8.5, 14},
		{render.PageTabloid, 11, 17},
		{render.PageA5, 5.8, 8.3},
		{render.PageA3, 11.7, 16.5},
		{render.PageA4, 8.3, 11.7},
		{"", 8.3, 11.7},
	}
	for _, tc := range cases {
		t.Run(tc.size, func(t *testing.T) {
			o := (&render.Job{PageSize: tc.size}).ToPDFOptions()
			require.NotNil(t, o.PaperWidth)
			require.NotNil(t, o.PaperHeight)
			assert.Equal(t, tc.w, *o.PaperWidth, "width")
			assert.Equal(t, tc.h, *o.PaperHeight, "height")
		})
	}
}

func TestToPDFOptions_Margins(t *testing.T) {
	cases := []struct {
		name                     string
		job                      render.Job
		top, bottom, left, right float64
	}{
		{"minimal", render.Job{MarginStyle: render.MarginMinimal}, 0.2, 0.2, 0.2, 0.2},
		{"none", render.Job{MarginStyle: render.MarginNone}, 0, 0, 0, 0},
		{"standard", render.Job{MarginStyle: render.MarginStandard}, 0.4, 0.4, 0.4, 0.4},
		{"default", render.Job{}, 0.4, 0.4, 0.4, 0.4},
		{"custom", render.Job{MarginStyle: render.MarginCustom, MarginTop: 1.1, MarginBottom: 1.2, MarginLeft: 1.3, MarginRight: 1.4}, 1.1, 1.2, 1.3, 1.4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := tc.job.ToPDFOptions()
			require.NotNil(t, o.MarginTop)
			require.NotNil(t, o.MarginBottom)
			require.NotNil(t, o.MarginLeft)
			require.NotNil(t, o.MarginRight)
			assert.Equal(t, tc.top, *o.MarginTop, "top")
			assert.Equal(t, tc.bottom, *o.MarginBottom, "bottom")
			assert.Equal(t, tc.left, *o.MarginLeft, "left")
			assert.Equal(t, tc.right, *o.MarginRight, "right")
		})
	}
}

func TestToPDFOptions_Landscape(t *testing.T) {
	assert.True(t, (&render.Job{Landscape: true}).ToPDFOptions().Landscape)
	assert.False(t, (&render.Job{Landscape: false}).ToPDFOptions().Landscape)
}

// TestE2E_PDFGeometry checks the page size and orientation reach Chrome's output.
func TestE2E_PDFGeometry(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	cases := []struct {
		name      string
		size      string
		landscape bool
		w, h      float64 // inches, as rendered (after orientation)
	}{
		{"a4_portrait", render.PageA4, false, 8.3, 11.7},
		{"a4_landscape", render.PageA4, true, 11.7, 8.3},
		{"letter", render.PageLetter, false, 8.5, 11},
		{"legal", render.PageLegal, false, 8.5, 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := newInlineJob(t, `<html><body>x</body></html>`)
			job.PageSize = tc.size
			job.Landscape = tc.landscape
			res := engine.RenderJob(job)
			require.True(t, res.Success, "render: %v", res.Error)
			w, h := pdfMediaBox(t, res.Output)
			assert.InDelta(t, tc.w*72, w, 2, "width pt")
			assert.InDelta(t, tc.h*72, h, 2, "height pt")
			if tc.landscape {
				assert.Greater(t, w, h)
			}
		})
	}
}
