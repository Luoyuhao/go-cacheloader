package internal

import "sync"

var (
	DefaultMaxConcurrency = uint32(500)
	token                 chan struct{}
	once                  sync.Once // 控制token初始化
)

func AskForToken() {
	once.Do(func() {
		token = make(chan struct{}, DefaultMaxConcurrency)
	})
	token <- struct{}{}
}

func ReturnToken() {
	<-token
}
