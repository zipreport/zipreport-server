package zpt

import (
	"context"
	"errors"
	"fmt"
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
	var name string
	if req.RequestURI == "/" {
		name = DefaultScriptName
	} else {
		_, i := utf8.DecodeRuneInString(req.RequestURI)
		name = filepath.Clean(req.RequestURI[i:])

		// Remove leading slashes (zip paths don't have leading /)
		name = strings.TrimLeft(name, "/\\")

		// Reject parent directory traversal attempts
		if strings.HasPrefix(name, "..") || strings.Contains(name, "/..") || strings.Contains(name, "\\..") {
			resp.WriteHeader(http.StatusForbidden)
			return
		}
	}

	buf, err := z.Zpt.ReadFile(name)
	if err == nil {
		resp.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		resp.WriteHeader(http.StatusOK)
		_, err = resp.Write(buf)
		if err != nil {
			z.logger.Error(err, "error writing http response", log.KV{"address": z.Server.Addr})
		}
		return
	} else {
		z.logger.Warn("error serving file", log.KV{"uri": name})
	}
	resp.WriteHeader(http.StatusNotFound)
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
	z.logger.Info(fmt.Sprintf("Shutting down server"), log.KV{"address": z.Server.Addr})
	return z.Server.Shutdown(ctx)
}
