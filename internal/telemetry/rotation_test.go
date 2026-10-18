package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRun = "0123456789abcdef0123456789abcdef"

func files(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var result []os.DirEntry
	for _, e := range entries {
		if ownedName.MatchString(e.Name()) {
			result = append(result, e)
		}
	}
	return result
}

func TestRotationSizeTotalAgeAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	w := newRotator(dir, testRun)
	defer w.Close()
	w.maxFile = 100
	w.maxTotal = 300
	now := time.Now().UTC()
	w.now = func() time.Time { return now }
	line := []byte(strings.Repeat("x", 59) + "\n")
	for i := 0; i < 10; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	entries := files(t, dir)
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
	if total > 300 || len(entries) > 4 {
		t.Fatalf("size limit: %d bytes, %d files", total, len(entries))
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
	w.Close()
	old := filepath.Join(dir, entries[0].Name())
	past := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired file survived", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated file removed", err)
	}
}

func TestDailyRotationAndRestart(t *testing.T) {
	dir := t.TempDir()
	w := newRotator(dir, testRun)
	now := time.Now().UTC()
	w.now = func() time.Time { return now }
	if _, err := w.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if _, err := w.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if len(files(t, dir)) != 2 {
		t.Fatal("no daily rotation")
	}
	w2 := newRotator(dir, "abcdef0123456789abcdef012345678901")
	defer w2.Close()
	if _, err := w2.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	// A real run ID is 32 hexadecimal characters.
	all, err := os.ReadDir(dir)
	if err != nil || len(all) != 3 {
		t.Fatal(all, err)
	}
}

func TestStorageRetryAfterOneMinute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	w := newRotator(dir, testRun)
	defer w.Close()
	now := time.Now()
	w.now = func() time.Time { return now }
	if _, err := w.Write([]byte("{}\n")); err == nil {
		t.Fatal("accepted invalid directory")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if _, err := w.Write([]byte("{}\n")); err == nil {
		t.Fatal("retried too early")
	}
	now = now.Add(time.Second)
	if _, err := w.Write([]byte("{}\n")); err != nil {
		t.Fatal("did not recover", err)
	}
}
