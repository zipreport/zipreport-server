package zptserver

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

type ZptServer struct {
	Config *Configuration
	Server *http.Server
}

/**
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
func NewZptServer(cfg *Configuration, router *gin.Engine) *ZptServer {
	return &ZptServer{
		Config: cfg,
		Server: &http.Server{
			Addr:          fmt.Sprintf("%s:%d", cfg.Addr, cfg.Port),
			Handler:      router,
			ReadTimeout:  time.Duration(cfg.ReadTimeout) * time.Second,
			WriteTimeout: time.Duration(cfg.WriteTimeout) * time.Second,
		},
	}
}

/**
 * Run Server
 */
func (z *ZptServer) Run() error {
	if z.Config.TLS {
		return z.Server.ListenAndServeTLS(z.Config.SSLCertFile, z.Config.SSLKeyFile);
	} else {
		return z.Server.ListenAndServe();
	}
}

/**
 * Shutdown Server
 */
func (z *ZptServer) Shutdown(ctx context.Context) error {
	return z.Server.Shutdown(ctx)
}

