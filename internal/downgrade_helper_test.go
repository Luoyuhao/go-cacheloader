package internal

import (
	"time"

	"go.uber.org/atomic"
)

// 限流测试只需要测出是否存在竞态条件即可
func (suite *Suite) TestDowngraded() {
	DowngradeThreshold = 1000
	cancel := make(chan struct{})
	total := atomic.Uint64{}
	for i := 0; i < 10; i++ {
		go func() {
			millisecondTimer := time.NewTicker(5 * time.Millisecond)
			canceled := false
			for {
				select {
				case <-millisecondTimer.C:
					if !Downgraded() {
						total.Inc()
					}
				case <-cancel:
					canceled = true
					break
				}
				if canceled {
					break
				}
			}
		}()
	}
	time.Sleep(5 * time.Second)
	cancel <- struct{}{}
	suite.True((total.Load() / 5) < (DowngradeThreshold + DowngradeThreshold/5)) // 第一秒的统计会存在误差
}
