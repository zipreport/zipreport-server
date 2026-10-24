package zpt

import (
	"context"
	"sync"
	"time"
	"zipreport-server/pkg/monitor"

	"github.com/oddbit-project/blueprint"
	"github.com/oddbit-project/blueprint/log"
)

type ServerPool struct {
	mx       sync.Mutex
	slots    chan *int
	ctx      context.Context
	pool     []*ZptServer
	BasePort int

	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	metrics *monitor.Metrics
	logger  *log.Logger
}

func NewServerPoolWithContext(ctx context.Context, limit int, basePort int, m *monitor.Metrics, logger *log.Logger) *ServerPool {
	serverSlots := make(chan *int, limit)
	for i := 0; i < limit; i++ {
		serverSlots <- nil
	}
	return &ServerPool{
		slots:    serverSlots,
		ctx:      ctx,
		pool:     make([]*ZptServer, limit),
		BasePort: basePort,

		ReadTimeout:  ReadTimeout,
		WriteTimeout: WriteTimeout,

		metrics: m,
		logger:  logger,
	}
}

func NewServerPool(limit int, basePort int, m *monitor.Metrics, logger *log.Logger) *ServerPool {
	return NewServerPoolWithContext(context.Background(), limit, basePort, m, logger)
}

func (p *ServerPool) BuildServer(reader *ZptReader) *ZptServer {
	<-p.slots // read slot
	p.mx.Lock()
	for idx, val := range p.pool {
		if val != nil {
			continue
		}
		server := NewZptServer(reader, p.BasePort+idx, p.logger)
		server.Server.ReadTimeout = p.ReadTimeout
		server.Server.WriteTimeout = p.WriteTimeout
		if err := server.Listen(); err != nil {
			p.logger.Error(err, "ephemeral report port is held by another process; check baseHttpPort and net.ipv4.ip_local_reserved_ports", log.KV{"address": server.Server.Addr})
			continue
		}
		p.pool[idx] = server
		p.mx.Unlock()
		p.metrics.HttpServers.Inc()
		go func() {
			_ = server.Run() // blocking call; error logged inside Run()
			p.metrics.HttpServers.Dec()
		}()
		return server
	}

	// no slot could be bound
	p.mx.Unlock()
	p.slots <- nil // Return the slot
	return nil
}

func (p *ServerPool) RemoveServer(srv *ZptServer) bool {
	p.mx.Lock()
	defer p.mx.Unlock()
	for i, v := range p.pool {
		if srv == v {
			_ = srv.Shutdown(p.ctx)
			p.pool[i] = nil
			p.slots <- nil // recover slot
			return true
		}
	}
	return false
}

func (p *ServerPool) Shutdown() {
	p.mx.Lock()
	defer p.mx.Unlock()
	// clear channel
	for len(p.slots) > 0 {
		<-p.slots
	}
	// shutdown all servers
	for i, srv := range p.pool {
		if srv != nil {
			_ = srv.Shutdown(blueprint.ShutdownContext())
		}
		p.pool[i] = nil
	}
}
