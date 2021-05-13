package pool

import (
	"errors"
	"time"
)

var (
	ErrInvalidCapacity        = errors.New("invalid capacity settings")
	ErrInvalidFactoryFunc     = errors.New("invalid factory func settings")
	ErrInvalidCloseFunc       = errors.New("invalid close func settings")
	ErrInvalidPingFunc        = errors.New("invalid ping func settings")
	ErrOpenNumber             = errors.New("numOpen > maxOpen")
	ErrConnIsNil              = errors.New("connection is nil. rejecting")
	ErrPoolClosed             = errors.New("pool is closed")
	ErrPoolClosedAndClose     = errors.New("connction pool is closed. close connection")
	ErrPoolNoActiveConnection = errors.New("no active connection")
)

// Config 连接池相关配置
type Config struct {
	//連接池中初始化的連接數(需>=0，若MaxCap>0則需<=MaxCap)
	InitialCap int
	//連接池中擁有的最大連接數(>=0，若為0表示無限制，若<0則代表不建立任何連線且忽略InitialCap設定，且Get/GetTry都會拿到nil, ErrPoolNoActiveConnection，Put/Ping/CloseGetPoolSize都不會拿到錯誤
	MaxCap int
	//生成连接的方法
	Factory func() (interface{}, error)
	//关闭连接的方法
	Close func(interface{}) error
	//检查连接是否有效的方法
	Ping func(interface{}) error
	//连接最大空闲时间，當Get時會檢查在pool內是否待超過IdleTimeout，若超過會close再建一個新的回傳
	IdleTimeout time.Duration
}

// Pool 基本方法
type Pool interface {
	Get() (interface{}, error)

	GetTry() (interface{}, error)

	Put(interface{}) error

	Ping(interface{}) error

	Close(interface{}) error

	Release()

	GetPoolSize() (InitialCap int, MaxCap int, Current int, Err error)
}
