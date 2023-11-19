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
		name = "index.html"
	} else {
		_, i := utf8.DecodeRuneInString(req.RequestURI)
		name = req.RequestURI[i:]
	}
	buf, err := z.Zpt.ReadFile(name)
	if err == nil {
		resp.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		resp.WriteHeader(http.StatusOK)
		_, err = resp.Write(buf)
		z.log.Err(err)
		return
	}
	resp.WriteHeader(http.StatusNotFound)
}

func (z *ZptServer) Run() error {
	z.log.Info().Msg(fmt.Sprintf("Starting server and listening on %s", z.Server.Addr))
	err := z.Server.ListenAndServe()
	// mask out shutdown as error
	if err != http.ErrServerClosed {
		z.log.Warn().Msgf("Server %s exited with error", z.Server.Addr)
		return err
	}
	return nil
}
func (z *ZptServer) Shutdown(ctx context.Context) error {
	z.log.Info().Msg(fmt.Sprintf("Shutting down server on %s", z.Server.Addr))
	return z.Server.Shutdown(ctx)
}
