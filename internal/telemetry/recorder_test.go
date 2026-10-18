package telemetry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

type bufferWriter struct{ bytes.Buffer }

func (w *bufferWriter) Close() error { return nil }
func decodeEvents(t *testing.T, data []byte) []Event {
	t.Helper()
	var result []Event
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var e Event
		err := decoder.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, e)
	}
	return result
}

func TestJSONLEnvelopeAndJournal(t *testing.T) {
	w := &bufferWriter{}
	var journal bytes.Buffer
	r := newRecorder(w, log.New(&journal, "", 0), "run", "boot")
	r.Record("run_started", map[string]any{"sample_interval_ns": int64(5e9)})
	r.Record("sample", map[string]any{"bytes": uint64(1234), "unavailable": nil})
	r.Record("cleanup_started", map[string]any{"cleanup_id": "cleanup-1"})
	r.Record("cleanup_finished", map[string]any{"cleanup_id": "cleanup-1", "error": "multiline\nerror"})
	r.Close()
	events := decodeEvents(t, w.Bytes())
	if len(events) != 4 {
		t.Fatal(len(events))
	}
	for i, e := range events {
		if e.SchemaVersion != 1 || e.Sequence != uint64(i+1) || e.RunID != "run" || e.BootID != "boot" || e.Timestamp.Location() != time.UTC {
			t.Fatal(e)
		}
	}
	if strings.Count(journal.String(), "\n") != 3 {
		t.Fatal(journal.String())
	}
	if strings.Count(w.String(), "\n") != 4 {
		t.Fatal("embedded newline broke JSONL")
	}
}

type blockingWriter struct {
	bufferWriter
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	return w.bufferWriter.Write(p)
}
func TestQueueOverflowIsNonblockingAndVisible(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	r := newRecorder(w, log.New(io.Discard, "", 0), "run", "boot")
	r.Record("sample", nil)
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("writer not started")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			r.Record("sample", nil)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Record blocked on disk")
	}
	if r.lost.Load() != 44 {
		t.Fatal(r.lost.Load())
	}
	close(w.release)
	r.Close()
	events := decodeEvents(t, w.Bytes())
	if len(events) != 257 || events[len(events)-1].DroppedTotal != 44 {
		t.Fatal("loss not recorded")
	}
}

type failingWriter struct {
	bufferWriter
	fail bool
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if !w.fail {
		w.fail = true
		return 0, errors.New("disk unavailable")
	}
	return w.bufferWriter.Write(p)
}
func TestWriteFailureIsReportedAndFollowingRecordsSurvive(t *testing.T) {
	w := &failingWriter{}
	var journal bytes.Buffer
	r := newRecorder(w, log.New(&journal, "", 0), "run", "boot")
	r.Record("sample", nil)
	r.Record("sample", nil)
	r.Close()
	e := decodeEvents(t, w.Bytes())
	if len(e) != 1 || e[0].Sequence != 2 || e[0].DroppedTotal != 1 || !strings.Contains(journal.String(), "monitoring continues") {
		t.Fatalf("%v %s", e, journal.String())
	}
}
