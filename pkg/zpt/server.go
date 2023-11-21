package zpt

import (
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"
)

const ReadTimeout = time.Duration(300) * time.Second
const WriteTimeout = time.Duration(300) * time.Second

const DefaultScriptName = "report.html"

var ErrAlreadyInitialized = errors.New("Server already initialized")
var ErrAlreadyShutdown = errors.New("Server already shutdown")

type ZptServer struct {
	Zpt    *ZptReader
	Server *http.Server
	Port   int
	log    zerolog.Logger
}

func NewZptServer(reader *ZptReader, port int, l zerolog.Logger, debug bool) *ZptServer {
	server := &ZptServer{
		Zpt:  reader,
		Port: port,
		log:  l,
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
		name = req.RequestURI[i:]
	}
	buf, err := z.Zpt.ReadFile(name)
	if err == nil {
		resp.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		resp.WriteHeader(http.StatusOK)
		_, err = resp.Write(buf)
		if err != nil {
			z.log.Err(err).
				Str("address", z.Server.Addr).
				Msg("error writing http response")
		}
		return
	} else {
		z.log.Warn().
			Err(err).
			Str("uri", name).
			Msg("error serving file")
	}
	resp.WriteHeader(http.StatusNotFound)
}

func (z *ZptServer) Run() error {
	z.log.Info().Msg(fmt.Sprintf("Starting server and listening on %s", z.Server.Addr))
	err := z.Server.ListenAndServe()
	// mask out shutdown as error
	if err != http.ErrServerClosed {
		z.log.Warn().
			Str("address", z.Server.Addr).
			Err(err).
			Msg("server exited with error")
		return err
	}
	return nil
}
func (z *ZptServer) Shutdown(ctx context.Context) error {
	z.log.Info().Str("address", z.Server.Addr).Msg("Shutting down server ")
	return z.Server.Shutdown(ctx)
}
