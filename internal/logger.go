package internal

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/sirupsen/logrus"

	"go.uber.org/atomic"
)

var (
	Err4Debug atomic.Value // 用于调试错误记录
	isDebug   bool
)

func DebugOn() {
	isDebug = true
}

const (
	prefix = "[go-cacheloader] "
)

// recover panic 并记录错误
func RecoverAndLog(ctx context.Context) {
	if e := recover(); e != nil {
		const size = 64 << 10
		buf := make([]byte, size)
		buf = buf[:runtime.Stack(buf, false)]
		if isDebug {
			Err4Debug.Store(fmt.Errorf("recover panic: {%v}", e))
			return
		}
		Err(ctx, fmt.Errorf("recover panic: {%v}", e))
		logrus.Fatalf(prefix+"goroutine panic: %v: %s", e, buf)
	}
}

// 收敛记录错误的方式（单测需要）
func Err(ctx context.Context, err error) {
	if isDebug {
		Err4Debug.Store(err)
	}
	logrus.Errorf(prefix+"location: %v, err: %v", location(), err.Error())
}

// 收敛记录错误的方式（单测需要）
func Warn(ctx context.Context, err error) {
	if isDebug {
		Err4Debug.Store(err)
	}
	logrus.Warnf(prefix+"location: %v, err: %v", location(), err.Error())
}

// 调试日志
func Debug(ctx context.Context, msg string) {
	logrus.Infof(prefix+"location: %v, msg: %v", location(), msg)
}

func location() string {
	_, file, line, ok := runtime.Caller(2)
	if !ok {
		file = "???"
		line = 0
	}

	file = filepath.Base(file)
	return file + ":" + strconv.Itoa(line)
}
