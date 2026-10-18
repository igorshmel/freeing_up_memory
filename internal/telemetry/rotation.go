package telemetry

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

var ownedName = regexp.MustCompile(`^memory-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{32}-[0-9]+\.jsonl$`)

type rotator struct {
	dir, run          string
	file              *os.File
	size              int64
	day               string
	index             uint64
	retryAt           time.Time
	maxFile, maxTotal int64
	maxAge            time.Duration
	now               func() time.Time
}

func newRotator(dir, run string) *rotator {
	return &rotator{dir: dir, run: run, maxFile: 16 << 20, maxTotal: 256 << 20, maxAge: 7 * 24 * time.Hour, now: time.Now}
}

func (w *rotator) Write(data []byte) (int, error) {
	now := w.now().UTC()
	if now.Before(w.retryAt) {
		return 0, fmt.Errorf("log storage retry scheduled at %s", w.retryAt.Format(time.RFC3339))
	}
	if int64(len(data)) > w.maxFile {
		return 0, fmt.Errorf("record exceeds log file size limit")
	}
	if w.file == nil || w.size+int64(len(data)) > w.maxFile || w.day != now.Format("2006-01-02") {
		if err := w.rotate(now); err != nil {
			w.fail(now)
			return 0, err
		}
	}
	n, err := w.file.Write(data)
	w.size += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.fail(now)
	}
	return n, err
}

func (w *rotator) fail(now time.Time) {
	_ = w.Close()
	w.retryAt = now.Add(time.Minute)
}

func (w *rotator) rotate(now time.Time) error {
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(w.dir, 0700); err != nil {
		return err
	}
	// Reserve the entire next file so the total stays bounded as it grows.
	if err := w.prune(now, w.maxTotal-w.maxFile); err != nil {
		return err
	}
	w.index++
	name := fmt.Sprintf("memory-%s-%s-%06d.jsonl", now.Format("20060102T150405.000000000Z"), w.run, w.index)
	f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	w.file = f
	w.size = 0
	w.day = now.Format("2006-01-02")
	return nil
}

func (w *rotator) prune(now time.Time, budget int64) error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}
	type entry struct {
		name     string
		size     int64
		modified time.Time
	}
	var files []entry
	var total int64
	for _, e := range entries {
		if !ownedName.MatchString(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) >= w.maxAge {
			if err := os.Remove(filepath.Join(w.dir, e.Name())); err != nil {
				return err
			}
			continue
		}
		files = append(files, entry{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].modified.Equal(files[j].modified) {
			return files[i].name < files[j].name
		}
		return files[i].modified.Before(files[j].modified)
	})
	for _, f := range files {
		if total <= budget {
			break
		}
		if err := os.Remove(filepath.Join(w.dir, f.name)); err != nil {
			return err
		}
		total -= f.size
	}
	return nil
}

func (w *rotator) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
