
package pool

import (
	"fmt"
	"net"
	"testing"
	"time"
)

const benchmarkAddr = "127.0.0.1:65431"

// run a dummy server for benchmark tests
func init() {
	go func() {
		l, err := net.Listen("tcp", benchmarkAddr)
		if err != nil {
			panic(fmt.Sprintf("benchmark server failed to listen: %v", err))
		}
		defer l.Close()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				// A simple echo server
				buf := make([]byte, 128)
				for {
					n, err := conn.Read(buf)
					if err != nil {
						conn.Close()
						return
					}
					conn.Write(buf[:n])
				}
			}()
		}
	}()
	// Wait for server to start
	time.Sleep(time.Millisecond * 100)
}

func benchmarkPool(b *testing.B, pool Pool, parallel int) {
	b.SetParallelism(parallel)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			conn, err := pool.Get()
			if err != nil {
				b.Errorf("Get failed: %v", err)
				continue
			}

			// Simulate some work
			cn := conn.(net.Conn)
			cn.Write([]byte("hello"))
			buf := make([]byte, 128)
			cn.Read(buf)

			if err := pool.Put(conn); err != nil {
				b.Errorf("Put failed: %v", err)
			}
		}
	})
}

func BenchmarkPools(b *testing.B) {
	factory := func() (interface{}, error) { return net.Dial("tcp", benchmarkAddr) }
	close := func(v interface{}) error { return v.(net.Conn).Close() }

	config := &Config{
		InitialCap: 20,
		MaxCap:     100,
		Factory:    factory,
		Close:      close,
		IdleTimeout: 1 * time.Minute,
	}

	mutexPool, err := NewPool(config)
	if err != nil {
		b.Fatalf("Failed to create mutex pool: %v", err)
	}
	defer mutexPool.Release()

	channelPool, err := NewChannelPool(config)
	if err != nil {
		b.Fatalf("Failed to create channel pool: %v", err)
	}
	defer channelPool.Release()

	parallelism := []int{1, 4, 16, 64, 256, 1024}

	for _, p := range parallelism {
		b.Run(fmt.Sprintf("MutexPool-Parallelism-%d", p), func(b *testing.B) {
			benchmarkPool(b, mutexPool, p)
		})

		b.Run(fmt.Sprintf("ChannelPool-Parallelism-%d", p), func(b *testing.B) {
			benchmarkPool(b, channelPool, p)
		})
	}
}
