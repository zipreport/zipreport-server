package apiserver

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

const (
	DefaultReadTimeout  = 300
	DefaultWriteTimeout = 300
	DefaultPort         = 6543
)

type Configuration struct {
	Addr         string
	Port         int
	TLS          bool
	SSLCertFile  string
	SSLKeyFile   string
	ReadTimeout  int
	WriteTimeout int
	Debug        bool
	ApiKey       string
}

type ApiServer struct {
	Config *Configuration
	Server *http.Server
}

/*
*

	Build server configuration with sensible defaults
*/
func NewConfiguration() *Configuration {
	return &Configuration{
		Port:         DefaultPort,
		TLS:          false,
		ReadTimeout:  DefaultReadTimeout,
		WriteTimeout: DefaultWriteTimeout,
		Debug:        false,
	}
}

/***
 * Assemble new Server
 */
func NewApiServer(cfg *Configuration, router *gin.Engine) *ApiServer {
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

/**
 * Run Server
 */
func (z *ApiServer) Run() error {
	var err error
	if z.Config.TLS {
		err = z.Server.ListenAndServeTLS(z.Config.SSLCertFile, z.Config.SSLKeyFile)
	} else {
		err = z.Server.ListenAndServe()
	}
	// mask out shutdown as error
	if err != http.ErrServerClosed {
		return err
	}
	return nil
}

/**
 * Shutdown Server
 */
func (z *ApiServer) Shutdown(ctx context.Context) error {
	return z.Server.Shutdown(ctx)
}
