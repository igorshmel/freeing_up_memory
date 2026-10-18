package monitor

import (
	"context"
	"sync"
	"testing"
	"time"

	"freeing_up_memory/internal/memory"
)

type staticCollector struct{ snapshot memory.Snapshot }

func (c staticCollector) Read() memory.Snapshot { return c.snapshot }

type recorded struct {
	kind string
	data any
}
type events struct {
	mu     sync.Mutex
	items  []recorded
	notify chan struct{}
}

func (e *events) Record(k string, d any) {
	e.mu.Lock()
	e.items = append(e.items, recorded{k, d})
	e.mu.Unlock()
	select {
	case e.notify <- struct{}{}:
	default:
	}
}
func (e *events) all() []recorded {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]recorded(nil), e.items...)
}
func (e *events) wait(t *testing.T, p func(recorded) bool) recorded {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		for _, v := range e.all() {
			if p(v) {
				return v
			}
		}
		select {
		case <-e.notify:
		case <-timeout:
			t.Fatal("missing event")
		}
	}
}
func newEvents() *events { return &events{notify: make(chan struct{}, 1)} }

func TestCheckPreservesThresholdAndObserveOnly(t *testing.T) {
	for _, tc := range []struct {
		name             string
		free, available  uint64
		observe, missing bool
		calls            int
	}{
		{"below", (1 << 30) - 1, 6 << 30, false, false, 1},
		{"equal", 1 << 30, 0, false, false, 0},
		{"above", 2 << 30, 0, false, false, 0},
		{"observe", 0, 0, true, false, 0},
		{"missing", 0, 0, false, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := memory.Snapshot{RAM: &memory.RAM{Free: tc.free, Available: tc.available}}
			if tc.missing {
				s.RAM = nil
			}
			e := newEvents()
			m := New(staticCollector{s}, e, Options{SampleInterval: 5 * time.Second, ObserveOnly: tc.observe})
			calls := 0
			m.Cleanup = func() memory.CleanupResult {
				calls++
				return memory.CleanupResult{Success: false, Error: "fake failure"}
			}
			m.check(context.Background())
			if calls != tc.calls {
				t.Fatalf("cleanup calls: %d", calls)
			}
			for _, v := range e.all() {
				if v.kind == "cleanup_check" {
					d := v.data.(map[string]any)
					if d["available_below_2_gib"] != (tc.available < 2<<30) {
						t.Fatal(d)
					}
				}
			}
			if tc.observe {
				e.wait(t, func(v recorded) bool { return v.kind == "cleanup_would_run" })
			}
			if tc.calls > 0 {
				v := e.wait(t, func(v recorded) bool { return v.kind == "cleanup_finished" })
				if v.data.(map[string]any)["result"].(memory.CleanupResult).Error != "fake failure" {
					t.Fatal(v)
				}
			}
		})
	}
}

type timerRequest struct {
	duration time.Duration
	fire     chan time.Time
}
type fakeClock struct {
	ticks    chan time.Time
	requests chan timerRequest
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.requests <- timerRequest{d, ch}
	return ch
}
func (c *fakeClock) Ticker(d time.Duration) (<-chan time.Time, func()) { return c.ticks, func() {} }
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
		var zero T
		return zero
	}
}

func TestSamplingContinuesDuringCleanupAndNextCheckWaits(t *testing.T) {
	e := newEvents()
	c := &fakeClock{make(chan time.Time, 1), make(chan timerRequest, 2)}
	m := New(staticCollector{memory.Snapshot{RAM: &memory.RAM{Free: 0}}}, e, Options{SampleInterval: 5 * time.Second})
	m.Clock = c
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	m.Cleanup = func() memory.CleanupResult {
		entered <- struct{}{}
		<-release
		return memory.CleanupResult{Success: false, Error: "test"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	receive(t, entered)
	c.ticks <- time.Now()
	e.wait(t, func(v recorded) bool {
		return v.kind == "sample" && v.data.(map[string]any)["cleanup_in_progress"] == true
	})
	select {
	case <-c.requests:
		t.Fatal("check timer started before cleanup finished")
	default:
	}
	select {
	case <-entered:
		t.Fatal("parallel cleanup")
	default:
	}
	close(release)
	r := receive(t, c.requests)
	if r.duration != 60*time.Second {
		t.Fatal(r.duration)
	}
	r.fire <- time.Now()
	receive(t, entered)
	e.wait(t, func(v recorded) bool {
		return v.kind == "cleanup_finished" && v.data.(map[string]any)["cleanup_id"] == "cleanup-2"
	})
	cancel()
	receive(t, done)
}

type sequenceCollector struct {
	mu        sync.Mutex
	snapshots []memory.Snapshot
}

func (c *sequenceCollector) Read() memory.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.snapshots[0]
	if len(c.snapshots) > 1 {
		c.snapshots = c.snapshots[1:]
	}
	return s
}
func TestClockGapAndWallTimeChangeRemainVisible(t *testing.T) {
	m1, m2 := int64(10e9), int64(15e9)
	b1, b2 := int64(10e9), int64(315e9)
	first := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := &sequenceCollector{snapshots: []memory.Snapshot{{CapturedAt: first, MonotonicNS: &m1, BoottimeNS: &b1}, {CapturedAt: first.Add(-time.Hour), MonotonicNS: &m2, BoottimeNS: &b2}}}
	e := newEvents()
	m := New(c, e, Options{SampleInterval: 5 * time.Second})
	clock := &fakeClock{make(chan time.Time, 1), make(chan timerRequest, 1)}
	m.Clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); m.sampleLoop(ctx) }()
	e.wait(t, func(v recorded) bool { return v.kind == "sample" })
	clock.ticks <- time.Now()
	v := e.wait(t, func(v recorded) bool {
		return v.kind == "sample" && v.data.(map[string]any)["monotonic_interval_ns"] != nil
	})
	d := v.data.(map[string]any)
	if d["monotonic_interval_ns"] != int64(5e9) || d["boottime_interval_ns"] != int64(305e9) {
		t.Fatal(d)
	}
	cancel()
	receive(t, done)
}
