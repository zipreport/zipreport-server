package test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testFixtureFields = map[string]string{"script": "test.html", "page_size": "A4", "margins": "standard"}

// postRenderWith posts a zpt held in memory using the given client; an empty key sends no auth header.
func postRenderWith(client *http.Client, baseURL, key, name string, zpt []byte, fields map[string]string) (*http.Response, error) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	part, err := w.CreateFormFile("report", name)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(zpt); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if err = w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", baseURL+"/v2/render", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if key != "" {
		req.Header.Set("X-Auth-Key", key)
	}
	return client.Do(req)
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func stopServer(t *testing.T, ps *procServer) {
	t.Helper()
	require.NoError(t, ps.Cmd.Process.Signal(os.Interrupt))
	require.NoError(t, ps.Cmd.Wait())
}

// TestProcess_AuthOverHTTP tests that the real binary enforces the API key on /v2/render
func TestProcess_AuthOverHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	ps := startServerOpts(t, serverOpts{Level: "info", WriteTimeout: 600, BaseHttpPort: 31300})
	fixture, err := os.ReadFile("fixtures/test.zpt")
	require.NoError(t, err)
	client := &http.Client{Timeout: 60 * time.Second}

	resp, err := postRenderWith(client, ps.BaseURL, "", "test.zpt", fixture, testFixtureFields)
	require.NoError(t, err)
	_ = readAll(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "no key")

	resp, err = postRenderWith(client, ps.BaseURL, strings.Repeat("cd", 32), "test.zpt", fixture, testFixtureFields)
	require.NoError(t, err)
	_ = readAll(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "wrong key")

	resp, err = postRenderWith(client, ps.BaseURL, ps.Key, "test.zpt", fixture, testFixtureFields)
	require.NoError(t, err)
	body := readAll(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "right key")
	assert.True(t, strings.HasPrefix(string(body), "%PDF-"))
}

// selfSignedCert writes a fresh ECDSA self-signed cert (SAN 127.0.0.1, localhost) and its key to dir
func selfSignedCert(t *testing.T, dir string) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "zipreport-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

// TestProcess_TLS tests that apiServer.tlsEnable makes the real binary serve HTTPS only
func TestProcess_TLS(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	certPath, keyPath, pool := selfSignedCert(t, t.TempDir())
	ps := startServerOpts(t, serverOpts{
		Level: "info", WriteTimeout: 600, BaseHttpPort: 31320, TLS: true,
		APIExtra: map[string]any{"tlsEnable": true, "tlsCert": certPath, "tlsKey": keyPath},
	})
	fixture, err := os.ReadFile("fixtures/test.zpt")
	require.NoError(t, err)

	trusting := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := postRenderWith(trusting, ps.BaseURL, ps.Key, "test.zpt", fixture, testFixtureFields)
	require.NoError(t, err)
	body := readAll(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(string(body), "%PDF-"))

	// plain http against the TLS port: either a transport failure or Go's 400
	plain := &http.Client{Timeout: 10 * time.Second}
	resp, err = postRenderWith(plain, "http://"+strings.TrimPrefix(ps.BaseURL, "https://"), ps.Key, "test.zpt", fixture, testFixtureFields)
	if err == nil {
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		b, _ := io.ReadAll(resp.Body) // the server may reset the connection after the 400
		_ = resp.Body.Close()
		assert.Contains(t, string(b), "HTTP request to an HTTPS server")
	}

	untrusting := &http.Client{Timeout: 10 * time.Second}
	_, err = postRenderWith(untrusting, ps.BaseURL, ps.Key, "test.zpt", fixture, testFixtureFields)
	require.Error(t, err)
	var certErr x509.UnknownAuthorityError
	assert.ErrorAs(t, err, &certErr, "client without the cert must fail verification")
}

// scrape returns the unlabelled sample values of a Prometheus text exposition
func scrape(url string) (map[string]float64, error) {
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	m := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || strings.HasPrefix(line, "#") {
			continue
		}
		if v, err := strconv.ParseFloat(f[1], 64); err == nil {
			m[f[0]] = v
		}
	}
	return m, nil
}

// TestProcess_MetricsEndpoint tests that the real binary exposes request counters on the Prometheus endpoint
func TestProcess_MetricsEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	promPort := freePort(t)
	ps := startServerOpts(t, serverOpts{
		Level: "info", WriteTimeout: 600, BaseHttpPort: 31340,
		ZipExtra:   map[string]any{"enableMetrics": true},
		Prometheus: map[string]any{"host": "127.0.0.1", "port": promPort, "endpoint": "/metrics"},
	})
	url := fmt.Sprintf("http://127.0.0.1:%d/metrics", promPort)
	var before map[string]float64
	require.Eventually(t, func() bool {
		var err error
		before, err = scrape(url)
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "metrics endpoint did not come up")
	for _, n := range []string{"total_requests", "total_request_success", "total_request_error", "conversion_time_count"} {
		require.Contains(t, before, n)
	}

	fixture, err := os.ReadFile("fixtures/test.zpt")
	require.NoError(t, err)
	client := &http.Client{Timeout: 60 * time.Second}

	resp, err := postRenderWith(client, ps.BaseURL, ps.Key, "test.zpt", fixture, testFixtureFields)
	require.NoError(t, err)
	_ = readAll(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	bad := map[string]string{"script": "test.html", "page_size": "NOPE", "margins": "standard"}
	resp, err = postRenderWith(client, ps.BaseURL, ps.Key, "test.zpt", fixture, bad)
	require.NoError(t, err)
	_ = readAll(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// the success counter is bumped after the body is written, so wait for it
	var after map[string]float64
	require.Eventually(t, func() bool {
		after, err = scrape(url)
		return err == nil && after["total_request_success"] > before["total_request_success"]
	}, 10*time.Second, 100*time.Millisecond)

	assert.Equal(t, 2.0, after["total_requests"]-before["total_requests"], "total_requests counts every request that reaches renderAction")
	assert.Equal(t, 1.0, after["total_request_success"]-before["total_request_success"])
	// a 400 from buildRenderJob returns before FailedOps.Inc(): only engine failures and write errors count as errors
	assert.Equal(t, 0.0, after["total_request_error"]-before["total_request_error"])
	assert.Equal(t, 1.0, after["conversion_time_count"]-before["conversion_time_count"], "only the successful render is observed")
}

// TestProcess_ConsoleLogging tests that zipReport.enableConsoleLogging gates page console output in the binary's log
func TestProcess_ConsoleLogging(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	for i, enabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			rnd := make([]byte, 8)
			_, err := rand.Read(rnd)
			require.NoError(t, err)
			marker := "zpt-console-marker-" + hex.EncodeToString(rnd)
			zipBytes := buildZipFiles(t, map[string][]byte{
				"report.html": []byte(`<html><body><script>console.log("` + marker + `")</script>ready</body></html>`),
			})

			ps := startServerOpts(t, serverOpts{
				Level: "info", WriteTimeout: 600, BaseHttpPort: 31350 + i*10,
				ZipExtra: map[string]any{"enableConsoleLogging": enabled},
			})
			resp, err := postRenderWith(&http.Client{Timeout: 60 * time.Second}, ps.BaseURL, ps.Key, "r.zpt", zipBytes,
				map[string]string{"script": "report.html", "page_size": "A4", "margins": "standard", "js_event": "false"})
			require.NoError(t, err)
			body := readAll(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.True(t, strings.HasPrefix(string(body), "%PDF-"))

			stopServer(t, ps)
			if enabled {
				assert.Contains(t, ps.Out.String(), marker)
			} else {
				assert.NotContains(t, ps.Out.String(), marker)
			}
		})
	}
}
