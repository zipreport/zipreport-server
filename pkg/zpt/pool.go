package zpt

import (
	"context"
	"sync"
	"zipreport-server/pkg/monitor"

	"github.com/oddbit-project/blueprint/log"
)

type ServerPool struct {
	mx       sync.Mutex
	slots    chan *int
	ctx      context.Context
	pool     []*ZptServer
	BasePort int
	metrics  *monitor.Metrics
	logger   *log.Logger
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
		metrics:  m,
		logger:   logger,
	}
}

func NewServerPool(limit int, basePort int, m *monitor.Metrics, logger *log.Logger) *ServerPool {
	return NewServerPoolWithContext(context.Background(), limit, basePort, m, logger)
}

func (p *ServerPool) BuildServer(reader *ZptReader) *ZptServer {
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
		p.slots <- nil // Return the slot
		p.logger.Error(nil, "pool inconsistency: no available slot found")
		return nil
	}

	server := NewZptServer(reader, p.BasePort+idx, p.logger)
	p.pool[idx] = server
	p.mx.Unlock()
	p.metrics.HttpServers.Inc()
	go func() {
		server.Run() // blocking call
		p.metrics.HttpServers.Dec()
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
				p.metrics.HttpServers.Dec()
			}
		}
		p.pool[i] = nil
	}
}
