package zpt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
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

func (z *ZptServer) Run() error {
	z.logger.Info(fmt.Sprintf("Starting server and listening on %s", z.Server.Addr))

	err := z.Server.ListenAndServe()
	// mask out shutdown as error
	if !errors.Is(err, http.ErrServerClosed) {
		z.logger.Error(err, "server exited with error", log.KV{"address": z.Server.Addr})
		return err
	}
	return nil
}
func (z *ZptServer) Shutdown(ctx context.Context) error {
	z.logger.Info("Shutting down server", log.KV{"address": z.Server.Addr})
	return z.Server.Shutdown(ctx)
}
