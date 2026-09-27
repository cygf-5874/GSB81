// Package mvccview 在一条按写入顺序追加的留痕日志之上提供 KV 读写。
//
// store.go 只暴露「单版本」读写：Get 看到的是最新值。快照、按时刻读与版本
// 回收是叠加在这一层之上的能力，语义以 README.md 的「对外契约」一节为准。
package mvccview

import (
	"sync"
	"time"
)

// rec 是一次写入的留痕。t 是这次写入发生时注入时钟的读数，也就是它的
// 版本号；同一时刻的多次写入共享同一个版本号，但各占一条留痕。
// deleted 为 true 表示这是一条墓碑（Delete 留下的）。
type rec struct {
	t       time.Time
	key     string
	value   []byte
	deleted bool
}

// Store 是一条留痕日志之上的 KV。
//
// 零值不可直接使用，请用 NewStore 构造。Store 的全部方法都可以被多个
// goroutine 同时调用。
type Store struct {
	mu    sync.Mutex
	clock Clock
	log   []rec
	live  map[string][]byte
}

// NewStore 用给定时钟构造一个空的 Store；clock 为 nil 时使用 SystemClock。
func NewStore(clock Clock) *Store {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Store{clock: clock, live: map[string][]byte{}}
}

// Put 把 key 设为 value 的副本，并在当前版本号上留下一条留痕。
// value 为 nil 或空切片都合法。
func (s *Store) Put(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := append([]byte(nil), value...)
	s.log = append(s.log, rec{t: s.clock.Now(), key: key, value: v})
	s.live[key] = v
}

// Get 返回 key 的最新值。key 不存在、或已被 Delete 时返回 (nil, false)。
// 返回的字节切片与 Store 内部解耦。
func (s *Store) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.live[key]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

// Delete 删除 key：留下一条墓碑留痕，并让 Get 不再看到它。
// 删除一个不存在的 key 同样合法，也会留痕。
func (s *Store) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.live, key)
	s.log = append(s.log, rec{t: s.clock.Now(), key: key, deleted: true})
}
