package memory

import (
	"bytes"
	"fmt"
	"github.com/shirou/gopsutil/mem"
	"os/exec"
	"time"
)

const Threshold = 1.0 * 1024 * 1024 * 1024 // 1 GB

func GetFreeMemory() (uint64, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}
	return v.Free, nil
}

const CleanupCommand = "sync && echo 3 | sudo tee /proc/sys/vm/drop_caches"

type CleanupResult struct {
	DurationNS      int64  `json:"duration_ns"`
	Success         bool   `json:"success"`
	Error           string `json:"error,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// ExecuteCleanup preserves the existing command and privilege model.
func ExecuteCleanup() CleanupResult {
	cmd := exec.Command("sh", "-c", CleanupCommand)
	output := &limitedBuffer{limit: 8 * 1024}
	cmd.Stderr = output
	start := time.Now()
	err := cmd.Run()
	r := CleanupResult{DurationNS: time.Since(start).Nanoseconds(), Success: err == nil, Stderr: output.String(), StderrTruncated: output.truncated}
	if err != nil {
		r.Error = fmt.Sprintf("failed to clear cache: %v", err)
	}
	return r
}

func ClearCache() error {
	r := ExecuteCleanup()
	if !r.Success {
		return fmt.Errorf("%s", r.Error)
	}

	return nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Len() int       { return b.buffer.Len() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
