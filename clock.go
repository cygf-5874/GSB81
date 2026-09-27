package mvccview

import (
	"sync"
	"time"
)

// Clock 抽象时间来源。版本号取自注入时钟的读数，所以实现里一切时间
// 都必须经过它，测试才能用 FakeClock 精确驱动，不依赖墙钟快慢。
type Clock interface {
	// Now 返回当前时间。
	Now() time.Time
}

// SystemClock 是默认时钟，直接使用标准库。
type SystemClock struct{}

// Now 返回真实的墙上时间。
func (SystemClock) Now() time.Time { return time.Now() }

// FakeClock 是可精确驱动的时钟：只有 Advance 会让它前进，并记录
// Advance 被调用的次数，便于断言调用序列而不是墙钟快慢。
//
// FakeClock 本身是并发安全的。
type FakeClock struct {
	mu    sync.Mutex
	now   time.Time
	steps int
}

// NewFakeClock 新建一个停在 start 的假时钟。
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now 返回当前假时间。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance 把假时间推进 d，并记一次步进。允许传负数，用来模拟时钟回退。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.steps++
}

// Steps 返回 Advance 被调用的次数。
func (c *FakeClock) Steps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.steps
}
