package test

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/render"
)

// ownListeners returns the listening TCP addresses owned by this process.
func ownListeners(t *testing.T) map[string]bool {
	t.Helper()
	inodes := map[string]bool{}
	fds, err := filepath.Glob("/proc/self/fd/*")
	require.NoError(t, err)
	for _, fd := range fds {
		l, err := os.Readlink(fd)
		if err == nil && strings.HasPrefix(l, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] = true
		}
	}
	n := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		require.NoError(t, err)
		sc := bufio.NewScanner(fh)
		sc.Scan() // header
		for sc.Scan() {
			cols := strings.Fields(sc.Text())
			if len(cols) > 9 && cols[3] == "0A" && inodes[cols[9]] {
				n[cols[1]] = true
			}
		}
		_ = fh.Close()
	}
	return n
}

// killChrome SIGKILLs every process whose command line carries --remote-debugging-port.
func killChrome(t *testing.T) int {
	t.Helper()
	cmds, err := filepath.Glob("/proc/[0-9]*/cmdline")
	require.NoError(t, err)
	killed := 0
	for _, c := range cmds {
		b, err := os.ReadFile(c)
		if err != nil || !strings.Contains(string(b), "--remote-debugging-port") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(c, "/proc/"), "/cmdline"))
		if err != nil {
			continue
		}
		if p, err := os.FindProcess(pid); err == nil && p.Kill() == nil {
			killed++
		}
	}
	return killed
}

// TestRenderJob_BrowserCrashLeavesNoProxy tests that a job whose browser died closes its egress proxy
func TestRenderJob_BrowserCrashLeavesNoProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}

	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()
	defer engine.Shutdown()

	baseline := ownListeners(t)

	job := newInlineJob(t, `<html><body>never signals</body></html>`)
	job.UseJSEvent = true
	job.JobTimeoutS = 6
	done := make(chan *render.JobResult, 1)
	go func() { done <- engine.RenderJob(job) }()
	time.Sleep(1500 * time.Millisecond)
	// listeners the job added: its server and its egress proxy
	added := map[string]bool{}
	for a := range ownListeners(t) {
		if !baseline[a] {
			added[a] = true
		}
	}
	require.NotEmpty(t, added, "job should hold listeners while running")

	require.Positive(t, killChrome(t), "no Chrome process found to kill")

	select {
	case res := <-done:
		assert.False(t, res.Success, "job must fail when its browser dies")
	case <-time.After(15 * time.Second):
		t.Fatal("job did not finish after Chrome was killed")
	}

	leaked := func() []string {
		var l []string
		for a := range ownListeners(t) {
			if added[a] {
				l = append(l, a)
			}
		}
		return l
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(leaked()) > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	assert.Empty(t, leaked(), "egress proxy of the dead browser's job must be closed")

	res := engine.RenderJob(newInlineJob(t, `<html><body>ready</body></html>`))
	assert.True(t, res.Success, "engine must recover: %v", res.Error)
}
