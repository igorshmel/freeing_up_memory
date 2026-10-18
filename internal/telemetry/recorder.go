package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Event struct {
	SchemaVersion int       `json:"schema_version"`
	Timestamp     time.Time `json:"timestamp"`
	BootID        string    `json:"boot_id"`
	RunID         string    `json:"run_id"`
	Sequence      uint64    `json:"sequence"`
	Type          string    `json:"type"`
	DroppedTotal  uint64    `json:"dropped_records_total"`
	Data          any       `json:"data"`
}

// Recorder never waits for disk or stderr in Record. Data must be immutable
// after submission. Close is called only after producers have stopped.
type Recorder struct {
	mu      sync.Mutex
	closed  bool
	seq     uint64
	queue   chan Event
	done    chan struct{}
	lost    atomic.Uint64
	runID   string
	bootID  string
	writer  io.WriteCloser
	journal *log.Logger
}

func New(dir string, journal *log.Logger) (*Recorder, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("create run ID: %w", err)
	}
	run := hex.EncodeToString(b)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, fmt.Errorf("read boot ID: %w", err)
	}
	return newRecorder(newRotator(dir, run), journal, run, strings.TrimSpace(string(boot))), nil
}

func newRecorder(writer io.WriteCloser, journal *log.Logger, run, boot string) *Recorder {
	r := &Recorder{queue: make(chan Event, 256), done: make(chan struct{}), runID: run, bootID: boot, writer: writer, journal: journal}
	go r.consume()
	return r
}

func (r *Recorder) Record(kind string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.seq++
	e := Event{SchemaVersion: 1, Timestamp: time.Now().UTC(), BootID: r.bootID, RunID: r.runID, Sequence: r.seq, Type: kind, Data: data}
	select {
	case r.queue <- e:
	default:
		r.lost.Add(1)
	}
}

func (r *Recorder) consume() {
	defer close(r.done)
	defer r.writer.Close()
	var lastError time.Time
	for e := range r.queue {
		e.DroppedTotal = r.lost.Load()
		data, err := json.Marshal(e)
		if err == nil {
			data = append(data, '\n')
			if e.Type == "run_started" || e.Type == "run_finished" || e.Type == "cleanup_started" || e.Type == "cleanup_finished" || e.Type == "measurement_error" || e.Type == "measurement_status" {
				r.journal.Print(strings.TrimSuffix(string(data), "\n"))
			}
			var n int
			n, err = r.writer.Write(data)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
		}
		if err != nil {
			r.lost.Add(1)
			if lastError.IsZero() || time.Since(lastError) >= time.Minute {
				r.journal.Printf("telemetry write failed; monitoring continues; dropped_records_total=%d: %v", r.lost.Load(), err)
				lastError = time.Now()
			}
		}
	}
}

func (r *Recorder) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.queue)
	}
	r.mu.Unlock()
	<-r.done
	return nil
}
