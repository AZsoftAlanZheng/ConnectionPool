package pool

import (
	"fmt"
	"sync"
	"time"
)

type policyType int32

const (
	cachedOrNewConn policyType = 0 //有可用空闲连接则优先使用，没有则创建
	alwaysNewConn   policyType = 1 //不管有没有空闲连接都重新创建
)

// channelPool 存放连接信息
type channelPool struct {
	factory func() (interface{}, error)
	close   func(interface{}) error
	ping    func(interface{}) error

	sync.Mutex                   //锁，操作pool时用到
	freeConn     []*idleConn     //空闲连接
	waitingQueue []chan idleConn //阻塞请求队列，等连接数达到最大限制时，后续请求将插入此队列等待可用连接
	numActive    int             //正在使用的連線數
	numOpen      int             //已建立连接或等待建立连接数
	closed       bool            //pool是否關閉
	maxIdle      int             //最大空闲连接数
	maxOpen      int             //最大连接数
	idleTimeout  time.Duration   //连接最大空闲时间，超过该事件则将失效
	strategy     policyType
}

type idleConn struct {
	conn  interface{}
	inUse bool
	t     time.Time
}

// NewPool 初始化连接
func NewPool(poolConfig *Config) (Pool, error) {
	if poolConfig.InitialCap < 0 || poolConfig.MaxCap < 0 || poolConfig.InitialCap > poolConfig.MaxCap {
		return nil, ErrInvalidCapacity
	}
	if poolConfig.Factory == nil {
		return nil, ErrInvalidFactoryFunc
	}
	if poolConfig.Close == nil {
		return nil, ErrInvalidCloseFunc
	}

	cp := &channelPool{
		factory:     poolConfig.Factory,
		close:       poolConfig.Close,
		ping:        nil,
		freeConn:    make([]*idleConn, 0, poolConfig.MaxCap),
		numActive:   0,
		numOpen:     0,
		closed:      false,
		maxIdle:     poolConfig.InitialCap,
		maxOpen:     poolConfig.MaxCap,
		idleTimeout: poolConfig.IdleTimeout,
		strategy:    cachedOrNewConn,
	}

	if poolConfig.Ping != nil {
		cp.ping = poolConfig.Ping
	}

	for i := 0; i < poolConfig.InitialCap; i++ {
		conn, err := cp.factory()
		if err != nil {
			cp.Release()
			return nil, fmt.Errorf("factory is not able to fill the pool: %s", err)
		}
		cp.freeConn = append(cp.freeConn, &idleConn{conn: conn, inUse: false, t: time.Now()})
	}
	cp.numOpen = poolConfig.InitialCap

	return cp, nil
}

// Get 从pool中取一个连接
func (cp *channelPool) Get() (interface{}, error) {
	return cp.getWithBlock(true)
}

func (cp *channelPool) GetTry() (interface{}, error) {
	return cp.getWithBlock(false)
}

// Put 将连接放回pool中
// 如果pool已經關閉，會把連線關閉，回傳ErrPoolClosedAndClose
func (cp *channelPool) Put(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	if cp.closed {
		cp.close(conn)
		return ErrPoolClosedAndClose
	}

	fmt.Printf("before lock: %d", time.Now().UnixNano())
	cp.Lock()
	fmt.Printf("after lock: %d", time.Now().UnixNano())
	if cp.maxOpen > 0 && cp.numOpen > cp.maxOpen {
		cp.Unlock()
		return ErrOpenNumber
	}
	//有等待连接的请求则将连接发给它们，否则放入freeConn
	if c := len(cp.waitingQueue); c > 0 {
		req := cp.waitingQueue[0]
		// This copy is O(n) but in practice faster than a linked list.
		// TODO: consider compacting it down less often and
		// moving the base instead?
		fmt.Printf("before copy: %d", time.Now().UnixNano())
		copy(cp.waitingQueue, cp.waitingQueue[1:])
		fmt.Printf("after copy: %d", time.Now().UnixNano())
		cp.waitingQueue = cp.waitingQueue[:c-1]
		req <- idleConn{conn: conn, inUse: true, t: time.Now()}
	} else {
		cp.numActive--
		fmt.Printf("before append: %d", time.Now().UnixNano())
		cp.freeConn = append(cp.freeConn, &idleConn{conn: conn, inUse: false, t: time.Now()})
		fmt.Printf("after append: %d", time.Now().UnixNano())
	}
	fmt.Printf("before Unlock: %d", time.Now().UnixNano())
	cp.Unlock()
	fmt.Printf("after Unlock: %d", time.Now().UnixNano())
	return nil
}

// Ping 检查单条连接是否有效
func (cp *channelPool) Ping(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	if cp.ping == nil {
		return ErrInvalidPingFunc
	}
	return cp.ping(conn)
}

// Close 關閉一條連線，並將已開啟連線數減一
// 如果pool已經關閉，會把連線關閉，回傳ErrPoolClosedAndClose
func (cp *channelPool) Close(conn interface{}) error {
	if conn == nil {
		return ErrConnIsNil
	}
	if cp.closed {
		cp.close(conn)
		return ErrPoolClosedAndClose
	}

	cp.Lock()
	cp.numOpen--
	cp.Unlock()
	return cp.close(conn)
}

// Release 释放连接池中所有连接
func (cp *channelPool) Release() {
	cp.Lock()
	cp.closed = true
	cp.Unlock()

	for _, wrapConn := range cp.freeConn {
		cp.close((*wrapConn).conn)
	}
}

func (cp *channelPool) GetPoolSize() (InitialCap int, MaxCap int, Current int, Err error) {
	InitialCap = cp.maxIdle
	MaxCap = cp.maxOpen
	cp.Lock()
	if cp.closed {
		cp.Unlock()
		return 0, 0, 0, ErrPoolClosed
	}
	Current = cp.numActive
	cp.Unlock()
	return InitialCap, MaxCap, Current, nil
}

func (cp *channelPool) getWithBlock(block bool) (interface{}, error) {
	cp.Lock()
	if cp.closed {
		cp.Unlock()
		return nil, ErrPoolClosed
	}

	//从freeConn取一个空闲连接
	numFree := len(cp.freeConn)
	if cp.strategy == cachedOrNewConn && numFree > 0 {
		conn := cp.freeConn[0]
		copy(cp.freeConn, cp.freeConn[1:])
		cp.freeConn = cp.freeConn[:numFree-1]
		//判断是否超时，超时则丢弃
		if timeout := cp.idleTimeout; timeout > 0 {
			if conn.t.Add(timeout).Before(time.Now()) {
				//丢弃并关闭该连接
				cp.close(conn.conn)
				subconn, err := cp.factory()
				if err != nil {
					cp.Unlock()
					return nil, err
				}
				cp.numActive++
				ic := &idleConn{conn: subconn, inUse: true, t: time.Now()}
				cp.Unlock()
				return ic.conn, nil
			}
		}
		cp.numActive++
		conn.inUse = true
		cp.Unlock()
		return conn.conn, nil
	}

	//如果没有空闲连接，而且当前建立的连接数已经达到最大限制则将请求加入waitingQueue队列，
	//并阻塞在这里，直到其它协程将占用的连接释放或connectionOpenner创建
	if cp.maxOpen > 0 && cp.numOpen >= cp.maxOpen {
		if !block {
			cp.Unlock()
			return nil, nil
		}
		// Make the connRequest channel. It's buffered so that the
		// connectionOpener doesn't block while waiting for the req to be read.
		req := make(chan idleConn, 1)
		cp.waitingQueue = append(cp.waitingQueue, req)
		cp.Unlock()
		ret, ok := <-req //阻塞
		if !ok {
			return nil, ErrPoolClosed
		}
		ret.inUse = true
		return ret.conn, nil
	}

	cp.numOpen++ //上面说了numOpen是已经建立或即将建立连接数，这里还没有建立连接，只是乐观的认为后面会成功，失败的时候再将此值减1
	cp.Unlock()
	conn, err := cp.factory()
	if err != nil {
		cp.Lock()
		cp.numOpen--
		cp.Unlock()
		return nil, err
	}
	cp.numActive++
	ic := &idleConn{conn: conn, inUse: true, t: time.Now()}
	return ic.conn, nil
}
