package test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadTextSample(t *testing.T) []byte {
	t.Helper()
	sample, err := os.ReadFile(filepath.Join("fixtures", "text-sample.pdf"))
	require.NoError(t, err)
	return sample
}

func TestPDFText_FindsHeadingAndBody(t *testing.T) {
	sample := loadTextSample(t)
	assert.True(t, pdfHasText(sample, "Test Report"))
	assert.True(t, pdfHasText(sample, "This is a test PDF generation."))
}

func TestPDFText_RejectsAbsentText(t *testing.T) {
	sample := loadTextSample(t)
	assert.False(t, pdfHasText(sample, "Test Reporx"))
	assert.False(t, pdfHasText(sample, "This is not in the report"))
}

func TestPDFText_IgnoresInfoTitle(t *testing.T) {
	sample := loadTextSample(t)
	require.True(t, bytes.Contains(sample, []byte("/Title (Test Report)")))
	broken := bytes.ReplaceAll(sample, []byte("endstream"), []byte("endstreaX"))
	assert.False(t, pdfHasText(broken, "Test Report"))
}
