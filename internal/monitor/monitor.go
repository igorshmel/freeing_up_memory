package monitor

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"freeing_up_memory/internal/memory"
)

const CheckInterval = 60 * time.Second

type Collector interface{ Read() memory.Snapshot }
type Recorder interface{ Record(string, any) }
type Clock interface {
	After(time.Duration) <-chan time.Time
	Ticker(time.Duration) (<-chan time.Time, func())
}
type RealClock struct{}

func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (RealClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

type Options struct {
	SampleInterval time.Duration `json:"sample_interval_ns"`
	ObserveOnly    bool          `json:"observe_only"`
	LogDir         string        `json:"log_dir"`
}

type Monitor struct {
	Collector    Collector
	Recorder     Recorder
	Clock        Clock
	Cleanup      func() memory.CleanupResult
	Options      Options
	Updates      chan memory.Snapshot
	mu           sync.Mutex
	cleanupID    string
	cleanupCount uint64
}

func New(c Collector, r Recorder, o Options) *Monitor {
	return &Monitor{Collector: c, Recorder: r, Clock: RealClock{}, Cleanup: memory.ExecuteCleanup, Options: o, Updates: make(chan memory.Snapshot, 1)}
}

func (m *Monitor) Run(ctx context.Context) {
	m.Recorder.Record("run_started", map[string]any{"options": m.Options, "free_threshold_bytes": memory.Threshold, "check_interval_ns": CheckInterval.Nanoseconds(), "cleanup_command": memory.CleanupCommand})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.sampleLoop(ctx) }()
	go func() { defer wg.Done(); m.checkLoop(ctx) }()
	wg.Wait()
	m.Recorder.Record("run_finished", map[string]any{"reason": "shutdown"})
}

func (m *Monitor) sampleLoop(ctx context.Context) {
	ticks, stop := m.Clock.Ticker(m.Options.SampleInterval)
	defer stop()
	var previous *memory.Snapshot
	for {
		if ctx.Err() != nil {
			return
		}
		s := m.Collector.Read()
		id := m.activeCleanup()
		data := map[string]any{"snapshot": s, "cleanup_id": id, "cleanup_in_progress": id != ""}
		if previous != nil && previous.MonotonicNS != nil && s.MonotonicNS != nil {
			data["monotonic_interval_ns"] = *s.MonotonicNS - *previous.MonotonicNS
		}
		if previous != nil && previous.BoottimeNS != nil && s.BoottimeNS != nil {
			data["boottime_interval_ns"] = *s.BoottimeNS - *previous.BoottimeNS
		}
		m.Recorder.Record("sample", data)
		if (previous == nil && len(s.Errors) > 0) || (previous != nil && !reflect.DeepEqual(previous.Errors, s.Errors)) {
			m.Recorder.Record("measurement_status", map[string]any{"errors": s.Errors, "recovered": len(s.Errors) == 0})
		}
		previous = &s
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}

func (m *Monitor) checkLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		m.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.Clock.After(CheckInterval):
		}
	}
}

func (m *Monitor) check(ctx context.Context) {
	s := m.Collector.Read()
	// The tray gets fresh Free on the original check cadence. Never wait for UI.
	select {
	case m.Updates <- s:
	default:
		select {
		case <-m.Updates:
		default:
		}
		select {
		case m.Updates <- s:
		default:
		}
	}
	if s.RAM == nil {
		m.Recorder.Record("measurement_error", map[string]any{"snapshot": s, "action": "skip_cleanup_check"})
		return
	}
	need := s.RAM.Free < memory.Threshold
	m.Recorder.Record("cleanup_check", map[string]any{
		"snapshot": s, "free_below_1_gib": need, "observe_only": m.Options.ObserveOnly,
		"available_below_1_gib":   s.RAM.Available < 1<<30,
		"available_below_1_5_gib": s.RAM.Available < 3*(1<<29),
		"available_below_2_gib":   s.RAM.Available < 2*(1<<30),
	})
	if !need || ctx.Err() != nil {
		return
	}
	if m.Options.ObserveOnly {
		m.Recorder.Record("cleanup_would_run", map[string]any{"snapshot": s, "command": memory.CleanupCommand})
		return
	}
	m.mu.Lock()
	m.cleanupCount++
	id := fmt.Sprintf("cleanup-%d", m.cleanupCount)
	m.cleanupID = id
	m.mu.Unlock()
	m.Recorder.Record("cleanup_started", map[string]any{"cleanup_id": id, "snapshot": s, "command": memory.CleanupCommand})
	result := m.Cleanup()
	m.mu.Lock()
	m.cleanupID = ""
	m.mu.Unlock()
	after := m.Collector.Read()
	m.Recorder.Record("cleanup_finished", map[string]any{"cleanup_id": id, "snapshot": after, "before": s, "command": memory.CleanupCommand, "result": result})
}

func (m *Monitor) activeCleanup() string { m.mu.Lock(); defer m.mu.Unlock(); return m.cleanupID }

func (m *Monitor) MarkStall() {
	m.Recorder.Record("user_stall_marker", map[string]any{"meaning": "time of click, possibly after recovery", "cleanup_id": m.activeCleanup()})
}
