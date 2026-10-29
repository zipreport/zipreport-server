package test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/egress"
)

// The TestSquid_* tests run against a real Squid (test/upstream/docker-compose.yml). They only run inside the
// compose "tests" container (make test-upstream), which sits on the fixture's network; elsewhere they skip.
//
// The origin answers only requests that come from Squid's address, so a 200 from it proves the request went
// through the upstream proxy.
const (
	squidHost    = "squid.test"
	squidIP      = "203.0.113.2"
	originHost   = "origin.test"
	originIP     = "203.0.113.10"
	localOnly    = "local-only.test" // origin's address, known to the local lookup seam only: Squid cannot resolve it
	squidUser    = "zipreport"       // test/upstream/htpasswd
	squidPass    = "squid-test-only" // test-only credential
	squidCertPEM = "upstream/certs/origin.crt"
)

func requireSquid(t *testing.T) {
	t.Helper()
	if os.Getenv("ZIPREPORT_TEST_SQUID") != "1" {
		t.Skip("needs the Squid fixture: make test-upstream")
	}
}

func squidURL(userinfo string) string {
	if userinfo != "" {
		userinfo += "@"
	}
	return "http://" + userinfo + squidHost + ":3128"
}

// startSquidProxy starts a per-job egress proxy chained to the Squid fixture, dialling for real. Names are answered
// by the lookup seam: squid.test always, origin.test only when resolveOrigin is set.
func startSquidProxy(t *testing.T, userinfo, resolve string, resolveOrigin bool, extra map[string][]string) *proxyEnv {
	t.Helper()
	lookup := map[string][]string{squidHost: {squidIP}, localOnly: {originIP}}
	if resolveOrigin {
		lookup[originHost] = []string{originIP}
	}
	for k, v := range extra {
		lookup[k] = v
	}
	return startProxy(t, proxyOpts{upstream: mkUpstream(t, squidURL(userinfo), resolve), lookup: lookup})
}

func mapLookup(m map[string][]string) egress.LookupFunc {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func originTLS(t *testing.T) *tls.Config {
	t.Helper()
	pem, err := os.ReadFile(squidCertPEM)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pem))
	return &tls.Config{RootCAs: pool, ServerName: originHost, MinVersion: tls.VersionTLS12}
}

// connectTLSGet opens a CONNECT tunnel through env to target, speaks TLS to origin.test inside it and GETs /host.
func connectTLSGet(t *testing.T, env *proxyEnv, target string) (int, string) {
	t.Helper()
	c, _, code := rawConnect(t, env.addr, target, "")
	if code != http.StatusOK {
		return code, ""
	}
	tc := tls.Client(c, originTLS(t))
	require.NoError(t, tc.Handshake())
	_, err := io.WriteString(tc, "GET /host HTTP/1.1\r\nHost: "+originHost+"\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(tc), &http.Request{Method: http.MethodGet})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func getBody(t *testing.T, env *proxyEnv, u string) (int, string) {
	t.Helper()
	resp, err := env.client.Get(u)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestSquid_OriginRefusesDirect(t *testing.T) {
	requireSquid(t)
	// the premise of every other test: without Squid in the path the origin answers 403
	resp, err := http.Get("http://" + originIP + "/host")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestSquid_LocalModeConnect(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, squidUser+":"+squidPass, "local", true, nil)
	// Squid cannot resolve local-only.test: success proves the CONNECT named the vetted IP
	code, body := connectTLSGet(t, env, localOnly+":443")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, originHost, body)
	n, _ := env.proxy.UpstreamErrors()
	assert.Zero(t, n)
}

func TestSquid_LocalModePlainHTTP(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, squidUser+":"+squidPass, "local", true, nil)
	// rides CONNECT 203.0.113.10:80 through Squid (which cannot resolve the name); the origin still sees the page's Host
	code, body := getBody(t, env, "http://"+localOnly+"/host")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, localOnly, body)
}

func TestSquid_LocalModeRefusesUnresolvable(t *testing.T) {
	requireSquid(t)
	// origin.test is not in the local lookup: local mode never hands Squid a name
	env := startSquidProxy(t, squidUser+":"+squidPass, "local", false, nil)
	assert.Equal(t, http.StatusForbidden, connectCode(t, env, originHost+":443"))
	n, _ := env.proxy.UpstreamErrors()
	assert.Zero(t, n, "refused before Squid was asked")
}

func TestSquid_ByNameConnect(t *testing.T) {
	requireSquid(t)
	// origin.test is unresolvable locally; only Squid (Docker DNS) can resolve it
	env := startSquidProxy(t, squidUser+":"+squidPass, "upstream", false, nil)
	code, body := connectTLSGet(t, env, originHost+":443")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, originHost, body)
}

func TestSquid_ByNamePlainHTTP(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, squidUser+":"+squidPass, "upstream", false, nil)
	code, body := getBody(t, env, "http://"+originHost+"/host")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, originHost, body)
}

func TestSquid_ByNameIntranetRefusedBySquid(t *testing.T) {
	requireSquid(t)
	// intranet.test is reachable only from Squid and resolves to a private address there: in by-name mode the
	// boundary is Squid's ACLs (docs/configuration.md), and the fixture's squid.conf carries the documented ones
	env := startSquidProxy(t, squidUser+":"+squidPass, "upstream", false, nil)
	assert.Equal(t, http.StatusBadGateway, connectCode(t, env, "intranet.test:80"))
	n, last := env.proxy.UpstreamErrors()
	assert.Equal(t, 1, n)
	assert.True(t, strings.HasPrefix(last, "403"), last)

	code, body := getBody(t, env, "http://intranet.test/")
	assert.Equal(t, http.StatusForbidden, code, "Squid's refusal is relayed")
	assert.Contains(t, body, "ERR_ACCESS_DENIED")
}

func TestSquid_ByNameStillRefusesLocallyPrivate(t *testing.T) {
	requireSquid(t)
	// a name the local resolver answers with a private address never reaches Squid
	env := startSquidProxy(t, squidUser+":"+squidPass, "upstream", false, map[string][]string{"intranet.test": {"172.30.99.10"}})
	assert.Equal(t, http.StatusForbidden, connectCode(t, env, "intranet.test:80"))
	n, _ := env.proxy.UpstreamErrors()
	assert.Zero(t, n)
}

func TestSquid_WrongPassword(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, squidUser+":wrong", "local", true, nil)
	assert.Equal(t, http.StatusBadGateway, connectCode(t, env, originHost+":443"))
	n, last := env.proxy.UpstreamErrors()
	assert.Equal(t, 1, n)
	assert.True(t, strings.HasPrefix(last, "407"), last)
	assert.NotContains(t, last, "wrong")
}

func TestSquid_AuthProbe(t *testing.T) {
	requireSquid(t)
	extra := map[string][]string{"p1.test": {"203.0.113.97"}, "p2.test": {"203.0.113.98"}, "p3.test": {"203.0.113.99"}}
	for _, tc := range []struct {
		name, userinfo, probe string
		wantOriginCode        int
		wantFirstErr          string
	}{
		{"policy_407s_with_probe_do_not_latch", squidUser + ":" + squidPass, originHost + ":443", http.StatusOK, "407"},
		{"policy_407s_without_probe_latch", squidUser + ":" + squidPass, "", http.StatusBadGateway, "407"},
		{"wrong_password_with_probe_latches", squidUser + ":wrong", originHost + ":443", http.StatusBadGateway, "authProbe misconfigured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := mkUpstream(t, squidURL(tc.userinfo), "local")
			up.AuthProbe = tc.probe
			lookup := map[string][]string{squidHost: {squidIP}, localOnly: {originIP}, originHost: {originIP}}
			for k, v := range extra {
				lookup[k] = v
			}
			env := startProxy(t, proxyOpts{upstream: up, lookup: lookup})
			for i, d := range []string{"p1.test:443", "p2.test:443", "p3.test:443"} {
				assert.Equal(t, http.StatusBadGateway, connectCode(t, env, d))
				if i == 0 { // the probe this 407 triggered has finished
					_, last := env.proxy.UpstreamErrors()
					assert.True(t, strings.HasPrefix(last, tc.wantFirstErr), last)
				}
			}
			assert.Equal(t, tc.wantOriginCode, connectCode(t, env, originHost+":443"))
		})
	}
}

func TestSquid_NoCredentials(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, "", "local", true, nil)
	assert.Equal(t, http.StatusBadGateway, connectCode(t, env, originHost+":443"))
	_, last := env.proxy.UpstreamErrors()
	assert.True(t, strings.HasPrefix(last, "407"), last)
}

func TestSquid_UpstreamItselfUnreachableFromPage(t *testing.T) {
	requireSquid(t)
	env := startSquidProxy(t, squidUser+":"+squidPass, "local", true, nil)
	for _, target := range []string{squidHost + ":3128", squidIP + ":3128", squidIP + ":80"} {
		assert.Equal(t, http.StatusForbidden, connectCode(t, env, target), target)
	}
	code, _ := getBody(t, env, "http://"+squidIP+":3128/squid-internal-mgr/info")
	assert.Equal(t, http.StatusForbidden, code)
}

func TestSquid_RenderThroughUpstream(t *testing.T) {
	requireSquid(t)
	if testing.Short() {
		t.Skip("Skipping browser-based test in short mode")
	}
	for _, resolve := range []string{"local", "upstream"} {
		t.Run(resolve, func(t *testing.T) {
			_, engine, _, cancel := setupTestServer(t)
			defer cancel()
			defer engine.Shutdown()
			up := mkUpstream(t, squidURL(squidUser+":"+squidPass), resolve)
			engine.SetEgressPolicy(egress.Policy{Upstream: up})
			lookup := map[string][]string{squidHost: {squidIP}, localOnly: {originIP}}
			engine.SetEgressLookup(mapLookup(lookup))

			host := originHost // by name: only Squid resolves it
			if resolve == "local" {
				host = localOnly // only the local lookup resolves it: proves the tunnel named the vetted IP
			}
			index := `<!DOCTYPE html><html><head><meta charset="UTF-8"></head><body>
<div id="marker">MISSING</div>
<script src="http://` + host + `/marker.js"></script>
</body></html>`
			result := egressRenderJob(t, engine, map[string][]byte{"index.html": []byte(index)})
			require.NotNil(t, result)
			require.True(t, result.Success, "render failed: %v", result.Error)
			assert.True(t, pdfHasText(result.Output, "SQUID-MARKER-OK"), "marker.js must load through Squid")
			for _, b := range result.BlockedSamples { // Chrome's own background requests may be refused; the origin must not be
				assert.NotContains(t, b, host)
			}
		})
	}
}
