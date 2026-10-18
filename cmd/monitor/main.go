package main

import (
	"context"
	"flag"
	"fmt"
	"freeing_up_memory/internal/memory"
	"freeing_up_memory/internal/monitor"
	"freeing_up_memory/internal/systray"
	"freeing_up_memory/internal/telemetry"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	sample := flag.Duration("sample-interval", 5*time.Second, "Diagnostic sampling interval (positive duration)")
	dir := flag.String("log-dir", "", "JSONL directory (default: $XDG_STATE_HOME/freeing_up_memory/logs or $HOME/.local/state/freeing_up_memory/logs)")
	observe := flag.Bool("observe-only", false, "Collect measurements without sync or drop_caches")
	flag.Parse()
	if *sample <= 0 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "sample-interval must be positive; positional arguments are not supported")
		os.Exit(2)
	}
	if *dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if !filepath.IsAbs(base) {
			home, err := os.UserHomeDir()
			if err != nil {
				log.Fatal(err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		*dir = filepath.Join(base, "freeing_up_memory", "logs")
	}
	absolute, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	recorder, err := telemetry.New(absolute, log.Default())
	if err != nil {
		log.Fatal(err)
	}
	defer recorder.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	m := monitor.New(memory.NewCollector(), recorder, monitor.Options{SampleInterval: *sample, ObserveOnly: *observe, LogDir: absolute})
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	systray.InitializeTray(ctx, m, cancel)
	cancel()
	<-done
}
