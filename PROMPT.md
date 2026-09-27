读写接口已经稳定，现在轮到快照那一层了 —— mvccview 是 Go 1.24 的 MVCC 存储库，
只用标准库，自检走 `scripts/check.sh`（`check/` 是固定验收程序，别改）。

`store.go` 里的单版本读写是完整实现，别动它；`store_test.go` 的 15 个用例当前全绿。
`mvcc.go` 里 `Snapshot`、`AsOf`、`Compact` 都还是 panic。

任务：按 README「对外契约」的 9 条把这一层补出来，让既有用例与固定件全过。

验收：
- go build ./... 退出码 0；
- go test ./... 退出码 0（既有用例仍全绿）；
- go run ./check 退出码 0，10 个场景全过（snapshot 3 + asof 3 + compact 3 + race 1）。

约束：
1. 不改 `check/`、不改 `store.go`、不改 `store_test.go`、不改 `clock.go`。
2. 不引入第三方依赖，不许用 `time.Now()`，一切时间从注入的 `Clock` 取。
3. 并发结果必须与 `map` 迭代顺序无关。
