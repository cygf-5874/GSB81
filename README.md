# mvccview

Go 1.24 的 MVCC 存储库，只用标准库，`go.mod` 里没有任何 `require`。

底层是一条按写入顺序追加的留痕日志：`store.go` 暴露单版本读写（`Put` / `Get` / `Delete`，
`Get` 只看最新值），版本号取自注入时钟的读数。**快照、按时刻读、版本回收是叠加在这一层
之上的能力**，挂在 `mvcc.go` 的 `DB` 上（`DB` 内嵌 `*Store`，读写方法直接沿用底层实现），
语义以本文件「对外契约」一节为准。

## 怎么跑

```bash
go build ./...            # 退出码 0
go test ./...             # 既有用例（15 个）
go run ./check            # 固定验收程序（10 个场景）
go run ./check -list      # 列出全部场景
go run ./check -only snapshot
bash scripts/check.sh     # 等价于 go run ./check
```

前提：Go 1.24。`check/`、`scripts/` 是固定验收的一部分，**请勿修改**。

## 对外契约

下面 9 条是 `Store` 与 `View` 的对外契约。**它们是契约，不是「当前行为」的转述。**

1. **版本号**：`Put(key, value)` 在 `Clock.Now()` 对应的当前版本号上生效。同一时刻的多次
   写入属于**同一个版本**：后者覆盖前者的值，但两条都留痕。`Delete` 同样在当前版本号上留痕。
2. **`Snapshot() *View`**：返回之后的所有写入对它**不可见**；返回时创建时刻之前的写入都已经
   在里面（快照不早于创建时刻）。`View.Get` / `View.Scan` 只读；同一个 `View` 无论读多少次，
   结果都相同（一旦建立即冻结）。
3. **`AsOf(key string, t time.Time) ([]byte, bool)`**：读该时刻**最近一次**生效的值（含 `t`：
   恰好发生在 `t` 的写入可见）。满足以下任一条件时返回 `(nil, false)`：`t` 之前从未写入过
   这个 key；`t` 之前（含 `t`）最后一次操作是 `Delete`。返回的字节切片与内部解耦。
4. **`Compact(upto time.Time) int`**：只在**没有任何活动 `View` 的创建时刻早于 `upto`** 时才
   回收。回收规则：对每个 key，保留它「严格早于 `upto`」的最后一条留痕作为基线，删除更早的
   留痕；返回**被删除的留痕条数**。只要存在创建时刻早于 `upto` 的活动 `View`，就**一条都不回收**
   并返回 0。创建时刻恰好等于 `upto` 的 `View` 不算「早于」。
5. **回收之后**：仍然活动的旧 `View` 的读结果必须与回收前**逐字节相同**；`AsOf` 对 `t >= upto`
   的结果与回收前相同；对**已被回收且不再可达**的时刻返回 `false`（不得 panic，也不得返回陈旧值）。
6. **`View.Close()`**：`View` 必须显式 `Close`；`Close` **幂等**（重复调用不 panic、不报错）。
   `Close` 之后 `Get` / `Scan` 返回 `ErrViewClosed`（不 panic）。`Compact` 之前必须把所有 `View`
   计入引用，`Close` 之后释放：早释放会让 `Compact` 回收掉别人还要用的版本，晚释放会让版本永不回收。
7. **`Delete` 是墓碑**：`Get` 立即返回 `false`，但 `AsOf` 在删除**之前**的时刻仍能读到旧值。
8. **并发安全**：`Put` / `Snapshot` / `Compact` / `View.Get` / `View.Scan` / `View.Close` 可以被多个
   goroutine 同时调用，竞态下的结果仍须满足第 1–7 条（固定件用**通道栅栏**保证真并发）。
9. **确定性**：结果不得依赖 `map` 迭代顺序；`View.Scan` 按 key **字典序升序**返回。时间语义一律由
   注入时钟驱动，实现里不许直接读墙钟。

## 本次要补的一层

`store.go` / `clock.go` / `errors.go` 是完整实现：留痕日志、单版本读写、`Clock` 接口、
`SystemClock` 与可精确驱动的 `FakeClock`（`Advance` / `Steps`）都已就位。

`mvcc.go` 目前只有类型与签名（`DB` 内嵌 `*Store`，`Put` / `Get` / `Delete` 由底层提升上来，
开箱可用），`Snapshot` / `AsOf` / `Compact` 以及 `View.Get` / `View.Scan` / `View.Close`
的方法体都是 `panic("not implemented")`。本次要补的就是这一层。

## 公开接口

```go
type Clock interface{ Now() time.Time }
type SystemClock struct{}
type FakeClock struct{}                  // NewFakeClock / Now / Advance / Steps

type KVPair struct{ Key string; Value []byte }
type View struct{}

// 底层单版本 KV（已实现）
func NewStore(clock Clock) *Store        // clock 为 nil 时用 SystemClock
func (s *Store) Put(key string, value []byte)
func (s *Store) Get(key string) ([]byte, bool)
func (s *Store) Delete(key string)

// 快照层（本次要补）；DB 内嵌 *Store，Put / Get / Delete 由底层提升上来
func NewDB(clock Clock) *DB
func (d *DB) Snapshot() *View
func (d *DB) AsOf(key string, t time.Time) ([]byte, bool)
func (d *DB) Compact(upto time.Time) int

func (v *View) Get(key string) ([]byte, bool, error)
func (v *View) Scan() ([]KVPair, error)
func (v *View) Close()

var ErrViewClosed error
```

## 固定件场景（10 个）

| 分组 | 场景 | 覆盖 |
| --- | --- | --- |
| `snapshot` | `SNAP1_frozen_after_writes` | 契约 2、9：快照冻结、多次读一致、`Scan` 升序 |
| `snapshot` | `SNAP2_upto_creation_and_close_idempotent` | 契约 2、6：包含创建时刻之前的写入、`Close` 幂等、关闭后报错 |
| `snapshot` | `SNAP3_guard_single_version_read` | 基线守卫：新一层不得改变既有单版本读语义 |
| `asof` | `ASOF1_tombstone_before_and_after_delete` | 契约 3、7：删除前仍可读、删除时刻（含）起读不到 |
| `asof` | `ASOF2_never_written_and_same_instant_last_wins` | 契约 1、3：从未写入返回 `false`、同一时刻多写读法 |
| `asof` | `ASOF3_guard_delete_tombstone` | 基线守卫：墓碑语义在单版本读上成立 |
| `compact` | `COMP1_reclaim_block_release` | 契约 4、5：按基线回收、早快照拦截、`Close` 后释放 |
| `compact` | `COMP2_old_view_reads_unchanged_after_reclaim` | 契约 4、5：回收后旧 `View` 读结果逐字节相同 |
| `compact` | `COMP3_guard_repeated_put_current_value` | 基线守卫：重复 `Put` 后当前值取最后一次 |
| `race` | `RACE1_fenced_mixed_ops` | 契约 8：栅栏后写 / 读 / 回收混跑，结束后不变量成立 |

`--only <组名>`（`snapshot` / `asof` / `compact` / `race`）可以只跑一组。每个场景在独立 goroutine 里
执行，带 30 秒看门狗；panic 被逐场景捕获记 `FAIL` 后继续，**失败不早退**。判据全部是确定性的：
时间语义只用 `FakeClock`，并发用通道栅栏（凑齐全部 goroutine 才放行），不依赖墙钟或机器速度。

## 目录

```
go.mod                     module mvccview
clock.go                   Clock 接口 + SystemClock + FakeClock（已实现）
errors.go                  ErrViewClosed
store.go                   单版本 KV：Put / Get / Delete（已实现）
mvcc.go                    DB：快照 / AsOf / 版本回收（本次要补）
store_test.go              既有用例（15 个）
check/main.go              固定验收程序（勿改）
scripts/check.sh           固定验收入口（勿改）
```
