package zpt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/oddbit-project/blueprint/log"
)

const ReadTimeout = time.Duration(300) * time.Second
const WriteTimeout = time.Duration(300) * time.Second

const DefaultScriptName = "report.html"

// MaxDebugLogLines caps the request lines logged per server, i.e. per job. Report JS controls the request count.
const MaxDebugLogLines = 1000

const truncatedMarker = "…[truncated]"

type ZptServer struct {
	Zpt    *ZptReader
	Server *http.Server
	Port   int
	logger *log.Logger

	listeners []net.Listener

	HttpDebug       bool // log one line per request (zipReport.enableHttpDebugging)
	debugLogged     atomic.Int64
	debugSuppressed atomic.Int64
}

func NewZptServer(reader *ZptReader, port int, logger *log.Logger) *ZptServer {
	server := &ZptServer{
		Zpt:    reader,
		Port:   port,
		logger: logger,
		Server: &http.Server{
			Addr:         "localhost:" + strconv.Itoa(port),
			Handler:      nil,
			ReadTimeout:  ReadTimeout,
			WriteTimeout: WriteTimeout,
		},
	}
	server.Server.Handler = server
	return server
}

func (z *ZptServer) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	if !z.HttpDebug {
		z.serve(resp, req)
		return
	}
	rec := &statusRecorder{ResponseWriter: resp}
	start := time.Now()
	completed := false
	defer func() {
		// deferred so a panicking handler still leaves a line (completed=false);
		// net/http recovers the panic itself
		if z.debugLogged.Add(1) > MaxDebugLogLines {
			z.debugSuppressed.Add(1)
			return
		}
		z.logger.Info("zpt request", log.KV{
			"address":       z.Server.Addr,
			"method":        truncate(req.Method, 16),
			"path":          truncate(req.URL.Path, 256),
			"status":        rec.statusCode(),
			"bytes":         rec.bytes,
			"contentLength": rec.Header().Get("Content-Length"),
			"writeError":    rec.writeErr != nil,
			"completed":     completed,
			"durationMs":    time.Since(start).Milliseconds(),
		})
	}()
	z.serve(rec, req)
	completed = true
}

type statusRecorder struct {
	http.ResponseWriter
	status   int
	bytes    int64
	writeErr error
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	if err != nil && r.writeErr == nil {
		r.writeErr = err
	}
	return n, err
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + truncatedMarker
}

func (z *ZptServer) serve(resp http.ResponseWriter, req *http.Request) {
	if req.Host != z.Server.Addr {
		z.logger.Warn("rejected request with foreign host", log.KV{"host": req.Host, "address": z.Server.Addr})
		resp.WriteHeader(http.StatusMisdirectedRequest)
		return
	}

	// Use URL.Path (decoded, query stripped) rather than the raw RequestURI so
	// query strings on asset URLs don't break lookups and encoded traversal is
	// decoded before validation.
	var name string
	if req.URL.Path == "/" {
		name = DefaultScriptName
	} else {
		_, i := utf8.DecodeRuneInString(req.URL.Path)
		name = filepath.Clean(req.URL.Path[i:])

		// Remove leading slashes (zip paths don't have leading /)
		name = strings.TrimLeft(name, "/\\")

		// Reject parent directory traversal attempts
		if strings.HasPrefix(name, "..") || strings.Contains(name, "/..") || strings.Contains(name, "\\..") {
			resp.WriteHeader(http.StatusForbidden)
			return
		}
	}

	rc, size, err := z.Zpt.OpenFile(name)
	if err != nil {
		z.logger.Warn("error serving file", log.KV{"uri": name})
		if errors.Is(err, ErrFileTooLarge) {
			resp.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp.WriteHeader(http.StatusNotFound)
		return
	}
	defer func() { _ = rc.Close() }()

	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	resp.Header().Set("Content-Type", contentType)
	resp.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	resp.WriteHeader(http.StatusOK)
	if _, err = io.CopyN(resp, rc, size); err != nil {
		z.logger.Error(err, "error writing http response", log.KV{"address": z.Server.Addr})
	}
}

// Listen binds 127.0.0.1:PORT and, where IPv6 loopback exists, [::1]:PORT;
// the browser resolves "localhost" itself and may use either.
func (z *ZptServer) Listen() error {
	port := strconv.Itoa(z.Port)
	l4, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return err
	}
	z.listeners = []net.Listener{l4}
	l6, err := net.Listen("tcp", "[::1]:"+port)
	switch {
	case err == nil:
		z.listeners = append(z.listeners, l6)
	case errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, syscall.EAFNOSUPPORT):
		// no IPv6 loopback on this host
	default:
		_ = l4.Close()
		z.listeners = nil
		return err
	}
	return nil
}

func (z *ZptServer) Run() error {
	z.logger.Info(fmt.Sprintf("Starting server and listening on %s", z.Server.Addr))

	if len(z.listeners) == 0 {
		err := z.Server.ListenAndServe()
		// mask out shutdown as error
		if !errors.Is(err, http.ErrServerClosed) {
			z.logger.Error(err, "server exited with error", log.KV{"address": z.Server.Addr})
			return err
		}
		return nil
	}

	errs := make([]error, len(z.listeners))
	var wg sync.WaitGroup
	for i, l := range z.listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = z.Server.Serve(l)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		// mask out shutdown as error
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			z.logger.Error(err, "server exited with error", log.KV{"address": z.Server.Addr})
			return err
		}
	}
	return nil
}

func (z *ZptServer) Shutdown(ctx context.Context) error {
	z.logger.Info("Shutting down server", log.KV{"address": z.Server.Addr})
	if n := z.debugSuppressed.Load(); n > 0 {
		z.logger.Info("zpt requests not logged", log.KV{"address": z.Server.Addr, "suppressed": n, "limit": MaxDebugLogLines})
	}
	err := z.Server.Shutdown(ctx)
	for _, l := range z.listeners {
		_ = l.Close()
	}
	return err
}
