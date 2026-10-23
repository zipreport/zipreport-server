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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/oddbit-project/blueprint/log"
)

const ReadTimeout = time.Duration(300) * time.Second
const WriteTimeout = time.Duration(300) * time.Second

const DefaultScriptName = "report.html"

type ZptServer struct {
	Zpt    *ZptReader
	Server *http.Server
	Port   int
	logger *log.Logger

	listeners []net.Listener
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
	err := z.Server.Shutdown(ctx)
	for _, l := range z.listeners {
		_ = l.Close()
	}
	return err
}
