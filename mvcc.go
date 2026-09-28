package mvccview

import (
	"sort"
	"time"
)

// KVPair 是 Scan 返回的一对键值。
type KVPair struct {
	Key   string
	Value []byte
}

// View 是一次 Snapshot 得到的冻结只读视图。
//
// frozen 是创建时刻 live 状态的独立副本，因此之后的写入、删除乃至
// Compact 都不会影响它的读结果。createdAt 是创建时刻，Compact 据此
// 判断该视图是否仍需保护更早的留痕；closed 之后读返回 ErrViewClosed。
type View struct {
	db        *DB
	createdAt time.Time
	frozen    map[string][]byte
	closed    bool
}

// DB 在 Store 之上补出快照、按时刻读与版本回收。它内嵌 *Store，
// 因此 README 契约第 1 条里的 Put / Get / Delete 直接沿用底层实现。
type DB struct {
	*Store

	// views 登记尚未 Close 的快照。受 Store.mu 保护；快照的创建、
	// 关闭与写入、回收共用同一把锁，保证并发下语义仍然确定。
	views map[*View]struct{}
}

// NewDB 用给定时钟构造一个空的 DB。
func NewDB(clock Clock) *DB {
	return &DB{Store: NewStore(clock), views: map[*View]struct{}{}}
}

// Snapshot 冻结当前值，返回一个之后写入不可见的 View。
func (d *DB) Snapshot() *View {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clock.Now()
	frozen := make(map[string][]byte, len(d.live))
	for key, value := range d.live {
		frozen[key] = append([]byte(nil), value...)
	}
	v := &View{db: d, createdAt: now, frozen: frozen}
	d.views[v] = struct{}{}
	return v
}

// AsOf 返回 key 在时刻 t（含 t）最近一次生效的值。
func (d *DB) AsOf(key string, t time.Time) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 留痕按写入顺序追加：同一时刻的多次写入后者覆盖前者，所以正向
	// 扫描、不断记住符合 r.t <= t 的最后一条即可。
	var best *rec
	for i := range d.log {
		r := &d.log[i]
		if r.key != key || r.t.After(t) {
			continue
		}
		best = r
	}
	if best == nil || best.deleted {
		return nil, false
	}
	return append([]byte(nil), best.value...), true
}

// Compact 回收 upto 之前的中间版本，返回被回收的留痕条数。
func (d *DB) Compact(upto time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 只要存在创建时刻严格早于 upto 的活动视图，就一条都不回收。
	for v := range d.views {
		if v.createdAt.Before(upto) {
			return 0
		}
	}
	// 对每个 key 找到「严格早于 upto」的最后一条留痕（按追加序扫描，
	// 同一时刻多写时取最后一次）。只保留它以及 t >= upto 的留痕。
	keep := make(map[string]int, len(d.log))
	for i := range d.log {
		r := &d.log[i]
		if r.t.Before(upto) {
			keep[r.key] = i
		}
	}
	out := d.log[:0]
	removed := 0
	for i := range d.log {
		r := &d.log[i]
		if !r.t.Before(upto) || keep[r.key] == i {
			out = append(out, d.log[i])
			continue
		}
		removed++
	}
	d.log = out
	return removed
}

// Get 从冻结视图里读取 key；视图已 Close 时返回 ErrViewClosed。
func (v *View) Get(key string) ([]byte, bool, error) {
	v.db.mu.Lock()
	defer v.db.mu.Unlock()
	if v.closed {
		return nil, false, ErrViewClosed
	}
	value, ok := v.frozen[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

// Scan 按 key 字典序升序返回视图内的全部键值；视图已 Close 时返回 ErrViewClosed。
func (v *View) Scan() ([]KVPair, error) {
	v.db.mu.Lock()
	defer v.db.mu.Unlock()
	if v.closed {
		return nil, ErrViewClosed
	}
	keys := make([]string, 0, len(v.frozen))
	for key := range v.frozen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]KVPair, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, KVPair{Key: key, Value: append([]byte(nil), v.frozen[key]...)})
	}
	return pairs, nil
}

// Close 释放视图；对同一个 View 重复调用是幂等的。
func (v *View) Close() {
	v.db.mu.Lock()
	defer v.db.mu.Unlock()
	if v.closed {
		return
	}
	v.closed = true
	delete(v.db.views, v)
}
