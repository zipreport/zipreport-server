package test

import (
	"bytes"
	"regexp"
)

// parsePDFPageCount returns the number of pages in a PDF byte slice.
// Uses regex on raw PDF to count /Type /Page entries (excluding /Type /Pages).
func parsePDFPageCount(data []byte) int {
	// Match /Type /Page but not /Type /Pages
	re := regexp.MustCompile(`/Type\s*/Page\b[^s]`)
	matches := re.FindAll(data, -1)
	return len(matches)
}

// pdfContainsText checks if the PDF byte stream contains the given string.
// Works for uncompressed text streams in the PDF.
func pdfContainsText(data []byte, text string) bool {
	return bytes.Contains(data, []byte(text))
}

// isValidPDF checks the PDF header (%PDF-) and trailer (%%EOF).
func isValidPDF(data []byte) bool {
	if len(data) < 10 {
		return false
	}
	// Check header
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return false
	}
	// Check for EOF marker (may have trailing whitespace/newlines)
	trimmed := bytes.TrimRight(data, "\r\n\x00 ")
	return bytes.HasSuffix(trimmed, []byte("%%EOF"))
}
