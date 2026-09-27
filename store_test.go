package mvccview

import (
	"sync"
	"testing"
	"time"
)

// 既有用例：只覆盖单版本读写与 FakeClock。
// 快照、按时刻读、版本回收这一层不在本文件的覆盖范围内。
//
// 下面这些用例当前全部通过。

func newTestStore(t *testing.T) (*Store, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(time.Unix(1700000000, 0))
	return NewStore(clock), clock
}

func TestNewStoreIsEmpty(t *testing.T) {
	st, _ := newTestStore(t)
	if _, ok := st.Get("a"); ok {
		t.Fatalf("新建的 Store 里不该有任何 key")
	}
}

func TestPutThenGet(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	v, ok := st.Get("a")
	if !ok || string(v) != "1" {
		t.Fatalf("Put 之后应能读回：期望 ok=true value=1 实际 ok=%v value=%q", ok, v)
	}
}

func TestPutOverwrites(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	st.Put("a", []byte("2"))
	v, _ := st.Get("a")
	if string(v) != "2" {
		t.Fatalf("重复 Put 后应读到最后一次的值：期望 2 实际 %q", v)
	}
}

func TestGetMissingKeyReturnsFalse(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	v, ok := st.Get("b")
	if ok || v != nil {
		t.Fatalf("不存在的 key 应返回 (nil,false)：实际 ok=%v value=%q", ok, v)
	}
}

func TestGetReturnsIndependentCopy(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("abc"))
	v, _ := st.Get("a")
	v[0] = 'z'
	again, _ := st.Get("a")
	if string(again) != "abc" {
		t.Fatalf("改写 Get 的返回值不该影响 Store：期望 abc 实际 %q", again)
	}
}

func TestPutCopiesInputValue(t *testing.T) {
	st, _ := newTestStore(t)
	buf := []byte("abc")
	st.Put("a", buf)
	buf[0] = 'z'
	v, _ := st.Get("a")
	if string(v) != "abc" {
		t.Fatalf("Put 之后改写入参不该影响 Store：期望 abc 实际 %q", v)
	}
}

func TestDeleteRemovesKey(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	st.Delete("a")
	if _, ok := st.Get("a"); ok {
		t.Fatalf("Delete 之后 Get 应为 false")
	}
}

func TestDeleteMissingKeyIsNoop(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	st.Delete("b")
	if v, ok := st.Get("a"); !ok || string(v) != "1" {
		t.Fatalf("删除不存在的 key 不该影响其它 key：实际 ok=%v value=%q", ok, v)
	}
}

func TestDeleteThenPutAgain(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", []byte("1"))
	st.Delete("a")
	st.Put("a", []byte("2"))
	v, ok := st.Get("a")
	if !ok || string(v) != "2" {
		t.Fatalf("删除后再 Put 应能读到新值：实际 ok=%v value=%q", ok, v)
	}
}

func TestNilValueIsReadable(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("a", nil)
	v, ok := st.Get("a")
	if !ok || len(v) != 0 {
		t.Fatalf("nil 值应可读且长度为 0：实际 ok=%v len=%d", ok, len(v))
	}
}

func TestEmptyKeyIsValidKey(t *testing.T) {
	st, _ := newTestStore(t)
	st.Put("", []byte("empty"))
	v, ok := st.Get("")
	if !ok || string(v) != "empty" {
		t.Fatalf("空字符串是合法 key：实际 ok=%v value=%q", ok, v)
	}
}

func TestConcurrentPutGetDistinctKeys(t *testing.T) {
	st, _ := newTestStore(t)
	const workers, steps = 8, 200
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := string(rune('a' + i))
			for j := 0; j < steps; j++ {
				st.Put(key, []byte{byte('0' + j%10)})
				st.Get(key)
			}
			st.Put(key, []byte("final"))
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		key := string(rune('a' + i))
		v, ok := st.Get(key)
		if !ok || string(v) != "final" {
			t.Fatalf("并发结束后 key=%q 应为 final：实际 ok=%v value=%q", key, ok, v)
		}
	}
}

func TestFakeClockStartsAtGivenTime(t *testing.T) {
	start := time.Unix(1700000000, 0)
	clock := NewFakeClock(start)
	if !clock.Now().Equal(start) {
		t.Fatalf("假时钟应停在 start：期望 %v 实际 %v", start, clock.Now())
	}
}

func TestFakeClockAdvanceAndNegative(t *testing.T) {
	start := time.Unix(1700000000, 0)
	clock := NewFakeClock(start)
	clock.Advance(time.Second)
	if got := clock.Now(); !got.Equal(start.Add(time.Second)) {
		t.Fatalf("前进 1 秒后时间不对：期望 %v 实际 %v", start.Add(time.Second), got)
	}
	clock.Advance(-2 * time.Second)
	if got := clock.Now(); !got.Equal(start.Add(-time.Second)) {
		t.Fatalf("允许负数推进：期望 %v 实际 %v", start.Add(-time.Second), got)
	}
}

func TestFakeClockStepsCountsAdvances(t *testing.T) {
	clock := NewFakeClock(time.Unix(1700000000, 0))
	if got := clock.Steps(); got != 0 {
		t.Fatalf("未推进时应为 0 步：实际 %d", got)
	}
	clock.Advance(time.Millisecond)
	clock.Advance(-time.Millisecond)
	clock.Advance(0)
	if got := clock.Steps(); got != 3 {
		t.Fatalf("Steps 应等于 Advance 的调用次数：期望 3 实际 %d", got)
	}
}
