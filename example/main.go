package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	pool "github.com/AZsoftAlanZheng/ConnectionPool"
)

const addr string = "127.0.0.1:8910"

var poolInitNum int = 1
var poolNum int = 2
var workerNum int = 10
var workerTryGetNum int = 5
var idleTimeout time.Duration = time.Second

func main() {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGUSR1, syscall.SIGUSR2)
	go server()
	//等待tcp server启动
	time.Sleep(2 * time.Second)
	fmt.Println("使用: ctrl+c 退出服务")
	fmt.Println("pool初始值:", poolInitNum)
	fmt.Println("pool上限值:", poolNum)
	fmt.Println("worker數量:", workerNum)
	fmt.Println("workerTryGetNum:", workerTryGetNum)
	fmt.Println("idleTimeout:", idleTimeout)
	p := initPool()
	for i := 0; i < workerNum; i++ {
		go client(i, p, false)
	}
	for i := 0; i < workerTryGetNum; i++ {
		go client(workerNum+i, p, true)
	}
	<-c
	fmt.Println("關閉連接池…")
	p.Release()
	fmt.Println("服务退出")
}

func initPool() pool.Pool {
	//factory 创建连接的方法
	factory := func() (interface{}, error) { return net.Dial("tcp", addr) }

	//close 关闭连接的方法
	close := func(v interface{}) error { return v.(net.Conn).Close() }

	//创建一个连接池
	poolConfig := &pool.Config{
		InitialCap: poolInitNum,
		MaxCap:     poolNum,
		Factory:    factory,
		Close:      close,
		IdleTimeout: time.Second,
	}
	p, err := pool.NewPool(poolConfig)
	if err != nil {
		log.Fatal(err)
	}
	return p
}

func client(num int, p pool.Pool, try bool) {
	//从连接池中取得一个连接
	var v interface{}
	var err error
RETRY:
	if try {
		fmt.Println("num:", num, " GetTry()")
		v, err = p.GetTry()
		if err != nil {
			log.Fatal(err)
		}
		if v == nil {
			fmt.Println("num:", num, " GetTry: got the nil")
			return
		}
	} else {
		fmt.Println("num:", num, " Get()")
		v, err = p.Get()
		if err != nil {
			log.Fatal(err)
		}
	}

	//do something
	cn := v.(net.Conn)
	fmt.Println("num:", num, " Got the connction:", cn.LocalAddr())

	_, _, current, err := p.GetPoolSize()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("num:", num, "the number of active connections: ", current)

	// 如果連線有問題，可以關閉它，再重新取得一個新連線
	if _, err = cn.Write(nil); err != nil {
		p.Close(cn)
		goto RETRY
	}

	//将连接放回连接池中
	fmt.Println("num:", num, " Put()")
	err = p.Put(v)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("num:", num, " End")
}

func server() {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal("Error listening: ", err)
	}
	defer l.Close()
	fmt.Println("Listening on ", addr)
	for {
		conn, err := l.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err)
		}
		fmt.Printf("Accept the connection %s -> %s \n", conn.RemoteAddr(), conn.LocalAddr())
	}
}
