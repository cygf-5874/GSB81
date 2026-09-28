package mvccview

import (
	"sort"
	"sync"
	"time"
)

// KVPair 是 Scan 返回的一对键值。
type KVPair struct {
	Key   string
	Value []byte
}

// View 是一次 Snapshot 得到的冻结只读视图。创建那一刻的内容被整体
// 冻结下来，之后的写入与版本回收都不会改变它的读结果。
type View struct {
	db      *DB
	created time.Time
	data    map[string][]byte

	mu     sync.Mutex
	closed bool
}

// DB 在 Store 之上补出快照、按时刻读与版本回收。它内嵌 *Store，
// 因此 README 契约第 1 条里的 Put / Get / Delete 直接沿用底层实现。
type DB struct {
	*Store
	views map[*View]struct{}
}

// NewDB 用给定时钟构造一个空的 DB；clock 为 nil 时使用 SystemClock。
func NewDB(clock Clock) *DB {
	return &DB{Store: NewStore(clock), views: map[*View]struct{}{}}
}

// Snapshot 冻结当前值，返回一个之后写入不可见的 View。
// 冻结在底层互斥锁内完成：创建时刻之前的写入都在里面，之后的写入
// （即使版本号相同）一律不可见。
func (d *DB) Snapshot() *View {
	d.mu.Lock()
	defer d.mu.Unlock()
	data := make(map[string][]byte, len(d.live))
	for k, val := range d.live {
		data[k] = append([]byte(nil), val...)
	}
	v := &View{db: d, created: d.clock.Now(), data: data}
	d.views[v] = struct{}{}
	return v
}

// AsOf 返回 key 在时刻 t（含 t）最近一次生效的值。
// 同一时刻的多次写入取最后一条留痕；t 之前从未写入过、或最后一次
// 操作是 Delete 时返回 (nil, false)。
func (d *DB) AsOf(key string, t time.Time) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var best *rec
	for i := range d.log {
		r := &d.log[i]
		if r.key != key || r.t.After(t) {
			continue
		}
		if best == nil || !r.t.Before(best.t) {
			best = r
		}
	}
	if best == nil || best.deleted {
		return nil, false
	}
	return append([]byte(nil), best.value...), true
}

// Compact 回收 upto 之前的中间版本，返回被回收的留痕条数。
// 只要还有一个创建时刻早于 upto 的活动 View，就一条都不回收并返回 0。
// 否则对每个 key 保留它严格早于 upto 的最后一条留痕作为基线，删除更早的。
func (d *DB) Compact(upto time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	for v := range d.views {
		if v.created.Before(upto) {
			return 0
		}
	}
	// 每个 key 严格早于 upto 的最后一条留痕（按写入顺序）是基线，必须保留。
	baseline := map[string]int{}
	for i := range d.log {
		if d.log[i].t.Before(upto) {
			baseline[d.log[i].key] = i
		}
	}
	keep := make(map[int]bool, len(baseline))
	for _, i := range baseline {
		keep[i] = true
	}
	removed := 0
	kept := d.log[:0]
	for i, r := range d.log {
		if r.t.Before(upto) && !keep[i] {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	for i := len(kept); i < len(d.log); i++ {
		d.log[i] = rec{}
	}
	d.log = kept
	return removed
}

// Get 从冻结视图里读取 key；视图已 Close 时返回 ErrViewClosed。
func (v *View) Get(key string) ([]byte, bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, false, ErrViewClosed
	}
	val, ok := v.data[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), val...), true, nil
}

// Scan 按 key 字典序升序返回视图内的全部键值；视图已 Close 时返回 ErrViewClosed。
func (v *View) Scan() ([]KVPair, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, ErrViewClosed
	}
	keys := make([]string, 0, len(v.data))
	for k := range v.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]KVPair, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, KVPair{Key: k, Value: append([]byte(nil), v.data[k]...)})
	}
	return pairs, nil
}

// Close 释放视图；对同一个 View 重复调用是幂等的。
func (v *View) Close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	v.mu.Unlock()

	v.db.mu.Lock()
	delete(v.db.views, v)
	v.db.mu.Unlock()
}
