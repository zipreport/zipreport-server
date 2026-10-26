package test

import (
	"os"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/stretchr/testify/require"
)

func profileDirs(t *testing.T) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	entries, err := os.ReadDir(launcher.DefaultUserDataDirPrefix)
	if os.IsNotExist(err) {
		return out
	}
	require.NoError(t, err)
	for _, e := range entries {
		out[e.Name()] = struct{}{}
	}
	return out
}

func newProfileDirs(t *testing.T, before map[string]struct{}) []string {
	var left []string
	for n := range profileDirs(t) {
		if _, ok := before[n]; !ok {
			left = append(left, n)
		}
	}
	return left
}

func waitNoNewProfiles(t *testing.T, before map[string]struct{}, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var left []string
	for time.Now().Before(deadline) {
		if left = newProfileDirs(t, before); len(left) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Empty(t, left, "Chrome profile directories left behind")
}

func renderUntilOK(t *testing.T, engineRender func() bool) {
	t.Helper()
	ok := false
	for i := 0; i < 3 && !ok; i++ {
		ok = engineRender()
	}
	require.True(t, ok, "engine must recover after the browser was killed")
}

func TestEngine_RelaunchAndShutdownRemoveProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	if !sharedLauncherMode() {
		t.Skip("shared-launcher mode only applies in containers and CI")
	}
	before := profileDirs(t)

	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()

	require.True(t, engine.RenderJob(newInlineJob(t, `<html><body>warm</body></html>`)).Success)
	require.Positive(t, killChrome(t), "no Chrome process found to kill")
	time.Sleep(500 * time.Millisecond)
	renderUntilOK(t, func() bool {
		return engine.RenderJob(newInlineJob(t, `<html><body>after crash</body></html>`)).Success
	})

	engine.Shutdown()
	waitNoNewProfiles(t, before, 5*time.Second)
}

func TestEngine_DedicatedDiscardRemovesProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	if sharedLauncherMode() {
		t.Skip("dedicated-launcher mode only")
	}
	before := profileDirs(t)

	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()

	require.True(t, engine.RenderJob(newInlineJob(t, `<html><body>warm</body></html>`)).Success)
	require.Positive(t, killChrome(t), "no Chrome process found to kill")
	time.Sleep(500 * time.Millisecond)
	renderUntilOK(t, func() bool {
		return engine.RenderJob(newInlineJob(t, `<html><body>after crash</body></html>`)).Success
	})

	engine.Shutdown()
	waitNoNewProfiles(t, before, 10*time.Second)
}
