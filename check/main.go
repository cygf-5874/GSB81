// check 是本题的固定验收程序：逐场景打印 PASS/FAIL，结尾打印通过数。
//
//	go run ./check                      # 跑全部 10 个场景
//	go run ./check -list                # 只列出场景名
//	go run ./check -only snapshot       # 只跑某个分组
//	go run ./check -only asof/ASOF1_tombstone_before_and_after_delete
//
// 判据全部是确定性的：时间语义一律由 FakeClock 驱动，不使用墙钟；
// 并发场景用通道栅栏（凑齐全部 goroutine 再放行），不依赖机器速度。
//
// 请勿修改本程序。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mvccview"
)

const scenarioTimeout = 30 * time.Second

func baseTime() time.Time { return time.Unix(1700000000, 0) }

type scenario struct {
	group string
	name  string
	fn    func() error
}

var allScenarios = []scenario{
	{"snapshot", "SNAP1_frozen_after_writes", snap1},
	{"snapshot", "SNAP2_upto_creation_and_close_idempotent", snap2},
	{"snapshot", "SNAP3_guard_single_version_read", snap3},
	{"asof", "ASOF1_tombstone_before_and_after_delete", asof1},
	{"asof", "ASOF2_never_written_and_same_instant_last_wins", asof2},
	{"asof", "ASOF3_guard_delete_tombstone", asof3},
	{"compact", "COMP1_reclaim_block_release", comp1},
	{"compact", "COMP2_old_view_reads_unchanged_after_reclaim", comp2},
	{"compact", "COMP3_guard_repeated_put_current_value", comp3},
	{"race", "RACE1_fenced_mixed_ops", race1},
}

func main() {
	var only string
	var list bool
	flag.StringVar(&only, "only", "", "只跑指定分组或场景（组名、组/名或场景名，逗号分隔）")
	flag.BoolVar(&list, "list", false, "只列出全部场景")
	flag.Parse()

	selected := selectScenarios(only)
	if len(selected) == 0 {
		fmt.Printf("没有匹配的场景：%q\n", only)
		os.Exit(2)
	}

	if list {
		for _, s := range selected {
			fmt.Printf("%s/%s\n", s.group, s.name)
		}
		return
	}

	pass := 0
	for _, s := range selected {
		err := runScenario(s)
		if err == nil {
			pass++
			fmt.Printf("PASS %s/%s\n", s.group, s.name)
			continue
		}
		fmt.Printf("FAIL %s/%s  %s\n", s.group, s.name, err.Error())
	}
	fmt.Printf("结果：通过 %d/%d\n", pass, len(selected))
	if pass != len(selected) {
		os.Exit(1)
	}
}

func selectScenarios(only string) []scenario {
	if strings.TrimSpace(only) == "" {
		return allScenarios
	}
	want := map[string]bool{}
	for _, p := range strings.Split(only, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			want[p] = true
		}
	}
	var out []scenario
	for _, s := range allScenarios {
		if want[s.group] || want[s.name] || want[s.group+"/"+s.name] {
			out = append(out, s)
		}
	}
	return out
}

func runScenario(s scenario) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("期望=场景正常完成 实际=panic: %v", r)
			}
			done <- err
		}()
		err = s.fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(scenarioTimeout):
		return fmt.Errorf("期望=场景在 %v 内结束 实际=超时", scenarioTimeout)
	}
}

func newDB() (*mvccview.DB, *mvccview.FakeClock) {
	clock := mvccview.NewFakeClock(baseTime())
	return mvccview.NewDB(clock), clock
}

func viewDump(v *mvccview.View) (string, error) {
	pairs, err := v.Scan()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&b, "%s=%s;", p.Key, p.Value)
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// snapshot 组

// snap1：快照一旦建立即冻结：之后的写入、删除、新增都对它不可见，
// 同一个 View 多次读结果相同，Scan 按 key 升序。
func snap1() error {
	db, clock := newDB()
	db.Put("b", []byte("2"))
	db.Put("a", []byte("1"))
	db.Put("c", []byte("3"))
	clock.Advance(time.Second)

	v := db.Snapshot()

	db.Put("a", []byte("9"))
	db.Delete("b")
	db.Put("d", []byte("4"))

	if got, ok, err := v.Get("a"); err != nil || !ok || string(got) != "1" {
		return fmt.Errorf("期望=快照里 a=1（之后的覆盖不可见） 实际=ok=%v value=%q err=%v", ok, got, err)
	}
	if got, ok, err := v.Get("b"); err != nil || !ok || string(got) != "2" {
		return fmt.Errorf("期望=快照里 b=2（之后的删除不可见） 实际=ok=%v value=%q err=%v", ok, got, err)
	}
	if got, ok, err := v.Get("d"); err != nil || ok || got != nil {
		return fmt.Errorf("期望=快照里没有 d（之后的新增不可见） 实际=ok=%v value=%q err=%v", ok, got, err)
	}
	if got, _, _ := v.Get("a"); string(got) != "1" {
		return fmt.Errorf("期望=同一个 View 多次读结果相同 实际=%q", got)
	}

	dump, err := viewDump(v)
	if err != nil {
		return fmt.Errorf("期望=Scan 成功 实际=err=%v", err)
	}
	if dump != "a=1;b=2;c=3;" {
		return fmt.Errorf("期望=Scan 按 key 升序返回 a=1;b=2;c=3; 实际=%s", dump)
	}

	if got, ok := db.Get("a"); !ok || string(got) != "9" {
		return fmt.Errorf("期望=Store 当前 a=9（快照之后写入照常生效） 实际=ok=%v value=%q", ok, got)
	}
	return nil
}

// snap2：快照不早于创建时刻 —— 创建时刻之前的写入必须在里面；
// Close 幂等，Close 之后 Get / Scan 返回 ErrViewClosed。
func snap2() error {
	db, clock := newDB()
	db.Put("x", []byte("1"))
	clock.Advance(time.Second)

	v := db.Snapshot()
	if got, ok, err := v.Get("x"); err != nil || !ok || string(got) != "1" {
		return fmt.Errorf("期望=快照包含创建时刻之前写入的 x=1 实际=ok=%v value=%q err=%v", ok, got, err)
	}

	db.Put("x", []byte("2"))
	if got, ok, err := v.Get("x"); err != nil || !ok || string(got) != "1" {
		return fmt.Errorf("期望=创建之后的写入不可见（仍为 1） 实际=ok=%v value=%q err=%v", ok, got, err)
	}

	v.Close()
	v.Close()

	if _, _, err := v.Get("x"); !errors.Is(err, mvccview.ErrViewClosed) {
		return fmt.Errorf("期望=Close 之后 Get 返回 ErrViewClosed 实际=%v", err)
	}
	if _, err := v.Scan(); !errors.Is(err, mvccview.ErrViewClosed) {
		return fmt.Errorf("期望=Close 之后 Scan 返回 ErrViewClosed 实际=%v", err)
	}
	return nil
}

// snap3：基线守卫 —— 新一层不得改变既有单版本读语义。
func snap3() error {
	db, _ := newDB()
	db.Put("k", []byte("v1"))
	if got, ok := db.Get("k"); !ok || string(got) != "v1" {
		return fmt.Errorf("期望=无视图时单版本读返回最新值 v1 实际=ok=%v value=%q", ok, got)
	}
	db.Put("k", []byte("v2"))
	if got, ok := db.Get("k"); !ok || string(got) != "v2" {
		return fmt.Errorf("期望=覆盖之后读到 v2 实际=ok=%v value=%q", ok, got)
	}
	if got, _ := db.Get("k"); string(got) != "v2" {
		return fmt.Errorf("期望=重复读稳定为 v2 实际=%q", got)
	}
	return nil
}

// ---------------------------------------------------------------------------
// asof 组

// asof1：Delete 是墓碑 —— 删除前的时刻仍能读到旧值，删除时刻（含）起读不到。
func asof1() error {
	db, clock := newDB()
	t0 := clock.Now()
	db.Put("k", []byte("v1"))
	clock.Advance(time.Second)
	t1 := clock.Now()
	db.Delete("k")

	if _, ok := db.Get("k"); ok {
		return fmt.Errorf("期望=删除后单版本读为 false 实际=true")
	}
	if got, ok := db.AsOf("k", t0); !ok || string(got) != "v1" {
		return fmt.Errorf("期望=删除前的时刻仍能读到 v1 实际=ok=%v value=%q", ok, got)
	}
	if _, ok := db.AsOf("k", t1); ok {
		return fmt.Errorf("期望=删除时刻（含）读不到 实际=读到了")
	}
	if _, ok := db.AsOf("k", t1.Add(time.Hour)); ok {
		return fmt.Errorf("期望=删除之后读不到 实际=读到了")
	}
	return nil
}

// asof2：从未写入过的 key 返回 false；同一时刻的多次写入属于同一个版本，
// 读到最后一次的值，且更晚时刻的写入不影响该时刻的读法。
func asof2() error {
	db, clock := newDB()
	t0 := clock.Now()

	if _, ok := db.AsOf("ghost", t0); ok {
		return fmt.Errorf("期望=从未写入过的 key 返回 false 实际=true")
	}
	if _, ok := db.AsOf("ghost", t0.Add(time.Hour)); ok {
		return fmt.Errorf("期望=从未写入过的 key 在更晚时刻也返回 false 实际=true")
	}

	db.Put("k", []byte("a"))
	db.Put("k", []byte("b"))
	db.Put("k", []byte("c"))

	if got, ok := db.AsOf("k", t0); !ok || string(got) != "c" {
		return fmt.Errorf("期望=同一时刻多写读到最后一次 c 实际=ok=%v value=%q", ok, got)
	}
	if _, ok := db.AsOf("k", t0.Add(-time.Nanosecond)); ok {
		return fmt.Errorf("期望=写入时刻之前读不到 实际=读到了")
	}

	clock.Advance(time.Second)
	db.Put("k", []byte("d"))
	if got, ok := db.AsOf("k", t0); !ok || string(got) != "c" {
		return fmt.Errorf("期望=更晚时刻的写入不影响 t0 的读法（仍为 c） 实际=ok=%v value=%q", ok, got)
	}
	return nil
}

// asof3：基线守卫 —— Delete 的墓碑语义在单版本读上成立，且不影响其它 key。
func asof3() error {
	db, _ := newDB()
	db.Put("a", []byte("1"))
	db.Put("b", []byte("2"))
	db.Delete("a")

	if _, ok := db.Get("a"); ok {
		return fmt.Errorf("期望=Delete 之后 Get(a) 为 false 实际=true")
	}
	if got, ok := db.Get("b"); !ok || string(got) != "2" {
		return fmt.Errorf("期望=Delete 不影响其它 key 实际=ok=%v value=%q", ok, got)
	}
	db.Delete("a")
	return nil
}

// ---------------------------------------------------------------------------
// compact 组

// comp1：无更早的活动 View 时按「每个 key 保留 upto 之前最后一条留痕」回收；
// 有更早的活动 View 时一条都不回收；Close 释放引用后又能回收。
func comp1() error {
	db, clock := newDB()
	t0 := clock.Now()
	db.Put("a", []byte("a1")) // T0
	clock.Advance(time.Second)
	t1 := clock.Now()
	db.Put("a", []byte("a2")) // T1
	clock.Advance(time.Second)
	t2 := clock.Now()
	db.Put("b", []byte("b1")) // T2
	clock.Advance(time.Second)
	t3 := clock.Now()

	// 无活动快照：a 的 T0 留痕被回收，a 保留 T1、b 保留 T2 → 回收 1 条
	if n := db.Compact(t3); n != 1 {
		return fmt.Errorf("期望=无更早快照时回收 1 条 实际=%d", n)
	}
	if got, ok := db.AsOf("a", t3); !ok || string(got) != "a2" {
		return fmt.Errorf("期望=回收后 AsOf(a,t3)=a2 实际=ok=%v value=%q", ok, got)
	}
	if got, ok := db.AsOf("a", t2); !ok || string(got) != "a2" {
		return fmt.Errorf("期望=回收后 AsOf(a,t2)=a2 实际=ok=%v value=%q", ok, got)
	}
	if got, ok := db.AsOf("b", t2); !ok || string(got) != "b1" {
		return fmt.Errorf("期望=回收后 AsOf(b,t2)=b1 实际=ok=%v value=%q", ok, got)
	}
	if _, ok := db.AsOf("a", t0); ok {
		return fmt.Errorf("期望=已被回收且不再可达的时刻返回 false 实际=true")
	}

	// 造一个早于要求回收时刻的活动快照：回收必须一个都不做
	v := db.Snapshot() // 创建于 T3
	clock.Advance(time.Second)
	t4 := clock.Now()
	db.Put("a", []byte("a3")) // T4
	clock.Advance(time.Second)
	t5 := clock.Now()
	db.Put("a", []byte("a4")) // T5

	if n := db.Compact(t5); n != 0 {
		return fmt.Errorf("期望=有更早的活动快照时一个都不回收 实际=%d", n)
	}
	if got, ok := db.AsOf("a", t5); !ok || string(got) != "a4" {
		return fmt.Errorf("期望=被拦下时 AsOf(a,t5)=a4 实际=ok=%v value=%q", ok, got)
	}

	// Close 之后引用释放，又能回收：a 的 T1 留痕被回收 → 1 条
	v.Close()
	if n := db.Compact(t5); n != 1 {
		return fmt.Errorf("期望=Close 之后回收 1 条 实际=%d", n)
	}
	if got, ok := db.AsOf("a", t5); !ok || string(got) != "a4" {
		return fmt.Errorf("期望=回收后 AsOf(a,t5)=a4 实际=ok=%v value=%q", ok, got)
	}
	if got, ok := db.AsOf("a", t4); !ok || string(got) != "a3" {
		return fmt.Errorf("期望=回收后 AsOf(a,t4)=a3 实际=ok=%v value=%q", ok, got)
	}
	if got, ok := db.AsOf("b", t4); !ok || string(got) != "b1" {
		return fmt.Errorf("期望=回收后 AsOf(b,t4)=b1 实际=ok=%v value=%q", ok, got)
	}
	if _, ok := db.AsOf("a", t1); ok {
		return fmt.Errorf("期望=回收后更早且不再可达的时刻返回 false 实际=true")
	}
	return nil
}

// comp2：回收之后仍然活动的旧 View 读结果必须与回收前逐字节相同。
func comp2() error {
	db, clock := newDB()
	t0 := clock.Now()
	db.Put("a", []byte("1"))
	clock.Advance(time.Second)
	db.Put("a", []byte("2"))
	clock.Advance(time.Second)
	db.Put("b", []byte("x"))
	clock.Advance(time.Second)
	t3 := clock.Now()

	v := db.Snapshot() // 创建于 T3，不早于 upto
	before, err := viewDump(v)
	if err != nil {
		return fmt.Errorf("期望=回收前 Scan 成功 实际=err=%v", err)
	}
	if before != "a=2;b=x;" {
		return fmt.Errorf("期望=快照内容 a=2;b=x; 实际=%s", before)
	}

	if n := db.Compact(t3); n != 1 {
		return fmt.Errorf("期望=创建时刻不早于 upto 时回收 1 条 实际=%d", n)
	}

	after, err := viewDump(v)
	if err != nil {
		return fmt.Errorf("期望=回收后 Scan 成功 实际=err=%v", err)
	}
	if before != after {
		return fmt.Errorf("期望=回收后旧 View 读结果逐字节相同 实际=回收前 %s 回收后 %s", before, after)
	}

	if got, ok := db.AsOf("a", t3); !ok || string(got) != "2" {
		return fmt.Errorf("期望=回收后 AsOf(a,t3)=2 实际=ok=%v value=%q", ok, got)
	}
	if _, ok := db.AsOf("a", t0); ok {
		return fmt.Errorf("期望=已被回收且不再可达的时刻返回 false 实际=true")
	}
	return nil
}

// comp3：基线守卫 —— 重复 Put 后当前值取最后一次，Delete 之后当前值消失；
// 版本回收不得改变这些单版本读语义。
func comp3() error {
	db, _ := newDB()
	for i := 0; i < 5; i++ {
		db.Put("k", []byte{byte('a' + i)})
	}
	if got, ok := db.Get("k"); !ok || string(got) != "e" {
		return fmt.Errorf("期望=重复 Put 后当前值为最后一次 e 实际=ok=%v value=%q", ok, got)
	}
	db.Delete("k")
	if _, ok := db.Get("k"); ok {
		return fmt.Errorf("期望=Delete 之后当前值消失 实际=true")
	}
	return nil
}

// ---------------------------------------------------------------------------
// race 组

type errBox struct {
	mu   sync.Mutex
	errs []string
}

func (b *errBox) add(format string, a ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.errs = append(b.errs, fmt.Sprintf(format, a...))
}

func (b *errBox) first() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.errs) == 0 {
		return ""
	}
	return b.errs[0]
}

// race1：写者 / 读者 / 回收器在通道栅栏后同时开跑，结束后不变量仍成立。
func race1() error {
	const (
		writers    = 4
		readers    = 2
		steps      = 300
		goroutines = writers + readers + 1
	)

	db, clock := newDB()
	db.Put("seed", []byte("s"))
	clock.Advance(time.Second)
	now := clock.Now()

	var (
		wg      sync.WaitGroup
		box     errBox
		entered = make(chan struct{}, goroutines)
		release = make(chan struct{})
	)
	spawn := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 栅栏里的 goroutine 也要自己兜住 panic，否则会直接把整个
			// 验收程序打崩，后面的场景与结尾汇总都跑不到。
			defer func() {
				if r := recover(); r != nil {
					box.add("并发场景中的 goroutine panic: %v", r)
				}
			}()
			entered <- struct{}{}
			<-release
			fn()
		}()
	}

	for w := 0; w < writers; w++ {
		w := w
		spawn(func() {
			key := fmt.Sprintf("k%d", w)
			for j := 1; j <= steps; j++ {
				db.Put(key, []byte(strconv.Itoa(j)))
			}
		})
	}

	for r := 0; r < readers; r++ {
		spawn(func() {
			for j := 0; j < steps; j++ {
				v := db.Snapshot()
				first, ok1, err1 := v.Get("k0")
				second, ok2, err2 := v.Get("k0")
				if err1 != nil || err2 != nil {
					box.add("快照 Get 出错：%v / %v", err1, err2)
				}
				if ok1 != ok2 || string(first) != string(second) {
					box.add("同一 View 两次读不一致：%q(%v) 与 %q(%v)", first, ok1, second, ok2)
				}
				if ok1 {
					n, perr := strconv.Atoi(string(first))
					if perr != nil || n < 1 || n > steps {
						box.add("读到的值不是写者写过的 1..%d 之内的整数：%q", steps, first)
					}
				}
				pairs, serr := v.Scan()
				if serr != nil {
					box.add("快照 Scan 出错：%v", serr)
				}
				for i := 1; i < len(pairs); i++ {
					if pairs[i-1].Key >= pairs[i].Key {
						box.add("Scan 未按 key 升序：%q 排在 %q 之后", pairs[i-1].Key, pairs[i].Key)
						break
					}
				}
				v.Close()
				if _, _, cerr := v.Get("k0"); !errors.Is(cerr, mvccview.ErrViewClosed) {
					box.add("Close 之后 Get 未返回 ErrViewClosed：%v", cerr)
				}
				v.Close() // 幂等
			}
		})
	}

	spawn(func() {
		for j := 0; j < steps; j++ {
			if n := db.Compact(now); n < 0 {
				box.add("Compact 返回负数：%d", n)
			}
		}
	})

	for i := 0; i < goroutines; i++ {
		<-entered
	}
	close(release)
	wg.Wait()

	if msg := box.first(); msg != "" {
		return fmt.Errorf("期望=并发下不变量成立 实际=%s", msg)
	}

	for w := 0; w < writers; w++ {
		key := fmt.Sprintf("k%d", w)
		got, ok := db.Get(key)
		if !ok || string(got) != strconv.Itoa(steps) {
			return fmt.Errorf("期望=并发结束后当前值 %s=%d 实际=ok=%v value=%q", key, steps, ok, got)
		}
		if as, ok := db.AsOf(key, now); !ok || string(as) != strconv.Itoa(steps) {
			return fmt.Errorf("期望=并发结束后 AsOf(%s,now)=%d 实际=ok=%v value=%q", key, steps, ok, as)
		}
	}

	// 结束后新起的快照必须看到全部最终值，且按 key 升序
	v := db.Snapshot()
	defer v.Close()
	dump, err := viewDump(v)
	if err != nil {
		return fmt.Errorf("期望=结束后 Scan 成功 实际=err=%v", err)
	}
	want := ""
	for w := 0; w < writers; w++ {
		want += fmt.Sprintf("k%d=%d;", w, steps)
	}
	want += fmt.Sprintf("seed=s;")
	if dump != want {
		return fmt.Errorf("期望=结束后快照内容 %s 实际=%s", want, dump)
	}

	db.Put("final", []byte("Z"))
	if got, ok := db.Get("final"); !ok || string(got) != "Z" {
		return fmt.Errorf("期望=结束后写入仍生效 final=Z 实际=ok=%v value=%q", ok, got)
	}
	return nil
}
