package monitor_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"freeing_up_memory/internal/memory"
	"freeing_up_memory/internal/monitor"
	"freeing_up_memory/internal/telemetry"
)

type observedRecorder struct {
	recorder *telemetry.Recorder
	samples  chan struct{}
}

func (r observedRecorder) Record(kind string, data any) {
	r.recorder.Record(kind, data)
	if kind == "sample" {
		select {
		case r.samples <- struct{}{}:
		default:
		}
	}
}

// Exercises real Linux reads and JSONL delivery without GUI or privileged commands.
func TestObserveOnlyPipeline(t *testing.T) {
	dir := t.TempDir()
	r, err := telemetry.New(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := observedRecorder{r, make(chan struct{}, 8)}
	m := monitor.New(memory.NewCollector(), out, monitor.Options{SampleInterval: 20 * time.Millisecond, ObserveOnly: true, LogDir: dir})
	m.Cleanup = func() memory.CleanupResult { panic("observe-only invoked cleanup") }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	for i := 0; i < 4; i++ {
		select {
		case <-out.samples:
		case <-time.After(3 * time.Second):
			t.Fatal("sampling stalled")
		}
	}
	m.MarkStall()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown stalled")
	}
	r.Close()
	paths, err := filepath.Glob(filepath.Join(dir, "memory-*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatal(paths, err)
	}
	counts := make(map[string]int)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(f)
		for {
			var e struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			err := decoder.Decode(&e)
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				t.Fatal(err)
			}
			counts[e.Type]++
			if e.Type == "sample" {
				var d struct {
					Snapshot memory.Snapshot `json:"snapshot"`
				}
				if err := json.Unmarshal(e.Data, &d); err != nil {
					t.Fatal(err)
				}
				if d.Snapshot.RAM == nil || d.Snapshot.RAM.Total == 0 {
					t.Fatal("real RAM metrics missing", d.Snapshot.Errors)
				}
			}
		}
		f.Close()
	}
	if counts["sample"] < 4 || counts["cleanup_check"] != 1 || counts["user_stall_marker"] != 1 || counts["run_finished"] != 1 || counts["cleanup_started"] != 0 {
		t.Fatal(counts)
	}
}
