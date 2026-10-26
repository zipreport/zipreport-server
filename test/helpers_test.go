package test

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

var pdfMediaBoxRe = regexp.MustCompile(`/MediaBox\s*\[\s*([-0-9.]+)\s+([-0-9.]+)\s+([-0-9.]+)\s+([-0-9.]+)\s*\]`)

// pdfMediaBox returns the width and height, in points, of the first /MediaBox in the PDF.
func pdfMediaBox(t *testing.T, pdf []byte) (w, h float64) {
	t.Helper()
	m := pdfMediaBoxRe.FindSubmatch(pdf)
	require.NotNil(t, m, "no /MediaBox found")
	var v [4]float64
	for i := range v {
		f, err := strconv.ParseFloat(string(m[i+1]), 64)
		require.NoError(t, err)
		v[i] = f
	}
	return v[2] - v[0], v[3] - v[1]
}

// parsePDFPageCount returns the number of pages in a PDF byte slice.
// Uses regex on raw PDF to count /Type /Page entries (excluding /Type /Pages).
func parsePDFPageCount(data []byte) int {
	// Match /Type /Page but not /Type /Pages
	re := regexp.MustCompile(`/Type\s*/Page\b[^s]`)
	matches := re.FindAll(data, -1)
	return len(matches)
}

var (
	pdfStreamRe  = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	pdfBfcharRe  = regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`)
	pdfBfrangeRe = regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`)
	pdfPairRe    = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	pdfTripleRe  = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	pdfHexRe     = regexp.MustCompile(`<([0-9A-Fa-f]+)>`)
)

// pdfText decodes the text shown in a Chrome/Skia PDF. Content streams are
// Flate-compressed and show 2-byte glyph codes, which only the fonts' ToUnicode
// CMaps map back to characters. The font-to-CMap binding is not resolved: each
// CMap decodes all content, giving one string per CMap that may contain stray
// characters from other fonts but contains every run set in that font.
func pdfText(data []byte) []string {
	var cmaps []map[string]rune
	var contents [][]byte
	for _, m := range pdfStreamRe.FindAllSubmatch(data, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			continue
		}
		switch {
		case bytes.Contains(raw, []byte("begincmap")):
			cmap := map[string]rune{}
			for _, blk := range pdfBfcharRe.FindAllSubmatch(raw, -1) {
				for _, p := range pdfPairRe.FindAllSubmatch(blk[1], -1) {
					if r, ok := pdfHexRune(p[2]); ok {
						cmap[strings.ToUpper(string(p[1]))] = r
					}
				}
			}
			for _, blk := range pdfBfrangeRe.FindAllSubmatch(raw, -1) {
				for _, tr := range pdfTripleRe.FindAllSubmatch(blk[1], -1) {
					lo, err1 := strconv.ParseUint(string(tr[1]), 16, 32)
					hi, err2 := strconv.ParseUint(string(tr[2]), 16, 32)
					dst, ok := pdfHexRune(tr[3])
					if err1 != nil || err2 != nil || !ok || hi < lo || hi-lo > 0xFFFF {
						continue
					}
					for c := lo; c <= hi; c++ {
						cmap[fmt.Sprintf("%0*X", len(tr[1]), c)] = dst + rune(c-lo)
					}
				}
			}
			cmaps = append(cmaps, cmap)
		case bytes.Contains(raw, []byte("BT")):
			contents = append(contents, raw)
		}
	}

	var out []string
	for _, cmap := range cmaps {
		var sb strings.Builder
		for _, c := range contents {
			for _, h := range pdfHexRe.FindAllSubmatch(c, -1) {
				hexs := strings.ToUpper(string(h[1]))
				if len(hexs)%4 != 0 {
					continue
				}
				var tok []rune
				ok := true
				for i := 0; i < len(hexs); i += 4 {
					r, found := cmap[hexs[i:i+4]]
					if !found {
						ok = false
						break
					}
					tok = append(tok, r)
				}
				if ok {
					sb.WriteString(string(tok))
				}
			}
		}
		out = append(out, sb.String())
	}
	return out
}

// pdfHexRune decodes a hex UTF-16BE code point (single unit or surrogate pair).
func pdfHexRune(h []byte) (rune, bool) {
	v, err := strconv.ParseUint(string(h), 16, 32)
	if err != nil {
		return 0, false
	}
	if len(h) == 8 {
		return utf16.DecodeRune(rune(v>>16), rune(v&0xFFFF)), true
	}
	return rune(v), true
}

// pdfHasText reports whether any decoded text run contains text.
func pdfHasText(data []byte, text string) bool {
	for _, s := range pdfText(data) {
		if strings.Contains(s, text) {
			return true
		}
	}
	return false
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
