package pool

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// CshannelPool is a channel-based connection pool.
type ChannelPool struct {
	factory     func() (interface{}, error)
	close       func(interface{}) error
	ping        func(interface{}) error
	conns       chan *idleConn
	maxOpen     int
	numOpen     int32
	idleTimeout time.Duration
	closed      int32
	quitChan    chan struct{}
}

// NewChannelPool creates a new channel-based pool.
func NewChannelPool(poolConfig *Config) (Pool, error) {
	if poolConfig.MaxCap <= 0 {
		poolConfig.MaxCap = 1 // Default to 1 if not set
	}
	if poolConfig.InitialCap < 0 || poolConfig.InitialCap > poolConfig.MaxCap {
		return nil, ErrInvalidCapacity
	}
	if poolConfig.Factory == nil {
		return nil, ErrInvalidFactoryFunc
	}
	if poolConfig.Close == nil {
		return nil, ErrInvalidCloseFunc
	}

	p := &ChannelPool{
		factory:     poolConfig.Factory,
		close:       poolConfig.Close,
		ping:        poolConfig.Ping,
		conns:       make(chan *idleConn, poolConfig.MaxCap),
		maxOpen:     poolConfig.MaxCap,
		idleTimeout: poolConfig.IdleTimeout,
		quitChan:    make(chan struct{}),
	}

	// Create initial connections
	for i := 0; i < poolConfig.InitialCap; i++ {
		conn, err := p.factory()
		if err != nil {
			p.Release()
			return nil, fmt.Errorf("factory is not able to fill the pool: %s", err)
		}
		atomic.AddInt32(&p.numOpen, 1)
		p.conns <- &idleConn{conn: conn, t: time.Now()}
	}

	// Start background cleanup goroutine
	if p.idleTimeout > 0 {
		go p.cleanup()
	}

	return p, nil
}

func (p *ChannelPool) getConn(ctx context.Context) (*idleConn, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	// Try to get a connection from the channel
	select {
	case conn := <-p.conns:
		if conn == nil {
			return nil, ErrPoolClosed
		}
		// Check if the connection is idle timeout
		if p.idleTimeout > 0 && conn.t.Add(p.idleTimeout).Before(time.Now()) {
			p.close(conn.conn)
			atomic.AddInt32(&p.numOpen, -1)
			// Do not return, try to get/create a new one
		} else {
			return conn, nil
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		// Channel is empty, fall through to create or wait
	}

	// If we can create a new connection
	if atomic.LoadInt32(&p.numOpen) < int32(p.maxOpen) {
		// Increment numOpen first optimistically
		if atomic.AddInt32(&p.numOpen, 1) > int32(p.maxOpen) {
			// We lost the race, another goroutine created a connection.
			atomic.AddInt32(&p.numOpen, -1)
		} else {
			conn, err := p.factory()
			if err != nil {
				atomic.AddInt32(&p.numOpen, -1)
				return nil, err
			}
			return &idleConn{conn: conn, t: time.Now()}, nil
		}
	}

	// Pool is full, wait for a connection to be returned or context to be cancelled
	select {
	case conn := <-p.conns:
		if conn == nil {
			return nil, ErrPoolClosed
		}
		if p.idleTimeout > 0 && conn.t.Add(p.idleTimeout).Before(time.Now()) {
			p.close(conn.conn)
			atomic.AddInt32(&p.numOpen, -1)
			// Retry, but with a new context to avoid immediate timeout
			return p.getConn(context.Background())
		}
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Get retrieves a connection from the pool.
func (p *ChannelPool) Get() (interface{}, error) {
	// Using a background context means Get will wait indefinitely if the pool is full.
	conn, err := p.getConn(context.Background())
	if err != nil {
		return nil, err
	}
	return conn.conn, nil
}

// GetTry tries to get a connection without blocking.
func (p *ChannelPool) GetTry() (interface{}, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	select {
	case conn := <-p.conns:
		if p.idleTimeout > 0 && conn.t.Add(p.idleTimeout).Before(time.Now()) {
			p.close(conn.conn)
			atomic.AddInt32(&p.numOpen, -1)
			return nil, ErrPoolFull
		}
		return conn.conn, nil
	default:
		// Try to create a new one if there's space
		if atomic.LoadInt32(&p.numOpen) < int32(p.maxOpen) {
			if atomic.AddInt32(&p.numOpen, 1) <= int32(p.maxOpen) {
				conn, err := p.factory()
				if err != nil {
					atomic.AddInt32(&p.numOpen, -1)
					return nil, err
				}
				return conn, nil
			} else {
				atomic.AddInt32(&p.numOpen, -1)
			}
		}
		return nil, ErrPoolFull
	}
}

// Put returns a connection to the pool.
func (p *ChannelPool) Put(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	if atomic.LoadInt32(&p.closed) == 1 {
		p.close(conn)
		return ErrPoolClosedAndClose
	}

	select {
	case p.conns <- &idleConn{conn: conn, t: time.Now()}:
		return nil
	default:
		// Pool is full, close the connection
		p.close(conn)
		atomic.AddInt32(&p.numOpen, -1)
		return nil
	}
}

// Close closes a single connection and removes it from the pool.
func (p *ChannelPool) Close(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	atomic.AddInt32(&p.numOpen, -1)
	return p.close(conn)
}

// Release closes all connections in the pool.
func (p *ChannelPool) Release() {
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return // Already closed
	}

	close(p.quitChan) // Signal cleanup goroutine to exit
	close(p.conns)    // Close the channel

	for wrapConn := range p.conns {
		if wrapConn != nil {
			p.close(wrapConn.conn)
			atomic.AddInt32(&p.numOpen, -1)
		}
	}
}

// Ping checks the health of a connection.
func (p *ChannelPool) Ping(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	if p.ping == nil {
		return ErrInvalidPingFunc
	}
	return p.ping(conn)
}

// GetPoolSize returns the size details of the pool.
func (p *ChannelPool) GetPoolSize() (InitialCap int, MaxCap int, Current int, Err error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return 0, 0, 0, ErrPoolClosed
	}
	return cap(p.conns), p.maxOpen, int(atomic.LoadInt32(&p.numOpen)), nil
}

// cleanup periodically checks for and closes idle connections.
func (p *ChannelPool) cleanup() {
	// Check more frequently than the timeout, but not too often.
	checkInterval := p.idleTimeout / 2
	if checkInterval < time.Second {
		checkInterval = time.Second
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.quitChan:
			return
		case <-ticker.C:
			var tempConns []*idleConn
			drainLen := len(p.conns)
			for i := 0; i < drainLen; i++ {
				select {
				case conn := <-p.conns:
					tempConns = append(tempConns, conn)
				default:
				}
			}

			for _, conn := range tempConns {
				if conn != nil && conn.t.Add(p.idleTimeout).Before(time.Now()) {
					p.close(conn.conn)
					atomic.AddInt32(&p.numOpen, -1)
				} else if conn != nil {
					// Still valid, put it back
					p.conns <- conn
				}
			}
		}
	}
}
