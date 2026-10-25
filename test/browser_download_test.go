package test

import (
	"bytes"
	"io"
	"testing"
	"time"
	"zipreport-server/pkg/browser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyLimited(t *testing.T) {
	t.Run("over the limit", func(t *testing.T) {
		var dst bytes.Buffer
		_, err := browser.CopyLimited(&dst, io.NopCloser(bytes.NewReader(make([]byte, 101))), 100, time.Second)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds")
	})

	t.Run("exactly the limit", func(t *testing.T) {
		var dst bytes.Buffer
		n, err := browser.CopyLimited(&dst, io.NopCloser(bytes.NewReader(make([]byte, 100))), 100, time.Second)
		require.NoError(t, err)
		assert.Equal(t, int64(100), n)
		assert.Equal(t, 100, dst.Len())
	})

	t.Run("stalled source", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer func() { _ = pw.Close() }()
		start := time.Now()
		var dst bytes.Buffer
		_, err := browser.CopyLimited(&dst, pr, 100, 200*time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "stalled")
		assert.Less(t, time.Since(start), time.Second)
	})
}
