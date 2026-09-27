package mvccview

import "time"

// KVPair 是 Scan 返回的一对键值。
type KVPair struct {
	Key   string
	Value []byte
}

// View 是一次 Snapshot 得到的冻结只读视图。
type View struct{}

// DB 在 Store 之上补出快照、按时刻读与版本回收。它内嵌 *Store，
// 因此 README 契约第 1 条里的 Put / Get / Delete 直接沿用底层实现。
type DB struct {
	*Store
}

// NewDB 用给定时钟构造一个空的 DB。
func NewDB(clock Clock) *DB {
	return &DB{Store: NewStore(clock)}
}

// Snapshot 冻结当前值，返回一个之后写入不可见的 View。
func (d *DB) Snapshot() *View {
	panic("not implemented")
}

// AsOf 返回 key 在时刻 t（含 t）最近一次生效的值。
func (d *DB) AsOf(key string, t time.Time) ([]byte, bool) {
	panic("not implemented")
}

// Compact 回收 upto 之前的中间版本，返回被回收的留痕条数。
func (d *DB) Compact(upto time.Time) int {
	panic("not implemented")
}

// Get 从冻结视图里读取 key；视图已 Close 时返回 ErrViewClosed。
func (v *View) Get(key string) ([]byte, bool, error) {
	panic("not implemented")
}

// Scan 按 key 字典序升序返回视图内的全部键值；视图已 Close 时返回 ErrViewClosed。
func (v *View) Scan() ([]KVPair, error) {
	panic("not implemented")
}

// Close 释放视图；对同一个 View 重复调用是幂等的。
func (v *View) Close() {
	panic("not implemented")
}
