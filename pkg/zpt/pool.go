package zpt

import (
	"context"
	"github.com/rs/zerolog"
	"sync"
	"zipreport-server/pkg/monitor"
)

type ServerPool struct {
	mx       sync.Mutex
	slots    chan *int
	ctx      context.Context
	pool     []*ZptServer
	BasePort int
	log      zerolog.Logger
	metrics  *monitor.Metrics
}

func NewServerPoolWithContext(ctx context.Context, limit int, basePort int, l zerolog.Logger, m *monitor.Metrics) *ServerPool {
	serverSlots := make(chan *int, limit)
	for i := 0; i < limit; i++ {
		serverSlots <- nil
	}
	return &ServerPool{
		slots:    serverSlots,
		ctx:      ctx,
		pool:     make([]*ZptServer, limit),
		BasePort: basePort,
		metrics:  m,
		log:      l,
	}
}

func NewServerPool(limit int, basePort int, l zerolog.Logger, m *monitor.Metrics) *ServerPool {
	return NewServerPoolWithContext(context.Background(), limit, basePort, l, m)
}

func (p *ServerPool) BuildServer(reader *ZptReader, debug bool) *ZptServer {
	_ = <-p.slots // read slot
	p.mx.Lock()
	idx := -1
	for i, val := range p.pool {
		if val == nil {
			idx = i
			break
		}
	}
	if idx == -1 {
		// should never happen
		p.mx.Unlock()
		return nil
	}
	server := NewZptServer(reader, p.BasePort+idx, p.log, debug)
	p.pool[idx] = server
	p.mx.Unlock()
	p.metrics.IncHttpServers()
	go func() {
		server.Run() // blocking call
		p.metrics.DecHttpServers()
	}()
	return server
}

func (p *ServerPool) RemoveServer(srv *ZptServer) bool {
	p.mx.Lock()
	defer p.mx.Unlock()
	for i, v := range p.pool {
		if srv == v {
			srv.Shutdown(p.ctx)
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
			if srv.Shutdown(p.ctx) == nil {
				p.metrics.DecHttpServers()
			}
		}
		p.pool[i] = nil
	}
}
