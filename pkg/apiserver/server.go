package apiserver

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"net/http"
	"time"
)

const (
	DefaultReadTimeout  = 300
	DefaultWriteTimeout = 300
	DefaultPort         = 6543
)

type ApiOptions struct {
	Addr         string
	Port         int
	TLS          bool
	SSLCertFile  string
	SSLKeyFile   string
	ReadTimeout  int
	WriteTimeout int
	Debug        bool
	ApiKey       string
	UseMetrics   bool
	Context      context.Context
	Log          zerolog.Logger
}

type ApiServer struct {
	Config *ApiOptions
	Server *http.Server
}

func DefaultApiOptions(ctx context.Context, l zerolog.Logger) *ApiOptions {
	return &ApiOptions{
		Port:         DefaultPort,
		TLS:          false,
		ReadTimeout:  DefaultReadTimeout,
		WriteTimeout: DefaultWriteTimeout,
		Debug:        false,
		Context:      ctx,
		Log:          l,
		UseMetrics:   true,
	}
}

func NewApiServer(cfg *ApiOptions, router *gin.Engine) *ApiServer {
	return &ApiServer{
		Config: cfg,
		Server: &http.Server{
			Addr:         fmt.Sprintf("%s:%d", cfg.Addr, cfg.Port),
			Handler:      router,
			ReadTimeout:  time.Duration(cfg.ReadTimeout) * time.Second,
			WriteTimeout: time.Duration(cfg.WriteTimeout) * time.Second,
		},
	}
}

func (z *ApiServer) Run() error {
	var err error
	if z.Config.TLS {
		z.Config.Log.Info().
			Str("certFile", z.Config.SSLCertFile).
			Str("keyFile", z.Config.SSLKeyFile).
			Str("address", z.Server.Addr).
			Msg("starting API webserver with SSL")
		err = z.Server.ListenAndServeTLS(z.Config.SSLCertFile, z.Config.SSLKeyFile)
	} else {
		z.Config.Log.Info().
			Str("address", z.Server.Addr).
			Msg("starting API webserver")
		err = z.Server.ListenAndServe()
	}
	// mask out shutdown as error
	if err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (z *ApiServer) Shutdown(ctx context.Context) error {
	z.Config.Log.Info().
		Msg("shutting down API webserver")
	return z.Server.Shutdown(ctx)
}
