package memory

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/mem"
	"golang.org/x/sys/unix"
)

// Snapshot is a best-effort collection, not an atomic view of all kernel files.
// Missing optional sources are nil and have an entry in Errors.
type Snapshot struct {
	CapturedAt           time.Time         `json:"captured_at"`
	MonotonicNS          *int64            `json:"monotonic_ns"`
	BoottimeNS           *int64            `json:"boottime_ns"`
	CollectionDurationNS int64             `json:"collection_duration_ns"`
	RAM                  *RAM              `json:"ram"`
	Swap                 *Swap             `json:"swap"`
	Devices              *[]SwapDevice     `json:"swap_devices"`
	ZRAM                 map[string]ZRAM   `json:"zram"`
	MemoryPSI            *PSI              `json:"memory_psi"`
	IOPSI                *PSI              `json:"io_psi"`
	VMStat               map[string]uint64 `json:"vmstat"`
	PageSizeBytes        int               `json:"page_size_bytes"`
	Errors               map[string]string `json:"errors,omitempty"`
}

type RAM struct {
	Total        uint64  `json:"total_bytes"`
	Free         uint64  `json:"free_bytes"`
	Available    uint64  `json:"available_bytes"`
	Used         uint64  `json:"used_bytes"`
	UsedPercent  float64 `json:"used_percent"`
	Buffers      uint64  `json:"buffers_bytes"`
	Cached       uint64  `json:"cached_bytes"` // Excludes SReclaimable, unlike gopsutil.Cached.
	SReclaimable uint64  `json:"sreclaimable_bytes"`
	SUnreclaim   uint64  `json:"sunreclaim_bytes"`
	Dirty        uint64  `json:"dirty_bytes"`
	Writeback    uint64  `json:"writeback_bytes"`
	Shmem        uint64  `json:"shmem_bytes"`
}

type Swap struct {
	Total       uint64  `json:"total_bytes"`
	Used        uint64  `json:"used_bytes"`
	Free        uint64  `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type SwapDevice struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     uint64 `json:"size_bytes"`
	Used     uint64 `json:"used_bytes"`
	Priority int    `json:"priority"`
}

type ZRAM struct {
	Original   uint64 `json:"original_bytes"`
	Compressed uint64 `json:"compressed_bytes"`
	MemoryUsed uint64 `json:"memory_used_bytes"`
}

type Pressure struct {
	Avg10  float64 `json:"avg10_percent"`
	Avg60  float64 `json:"avg60_percent"`
	Avg300 float64 `json:"avg300_percent"`
	Total  uint64  `json:"total_us"`
}

type PSI struct {
	Some Pressure `json:"some"`
	Full Pressure `json:"full"`
}

// Collector has no mutable state and is safe for concurrent sampling and checks.
type Collector struct {
	ProcRoot      string
	SysRoot       string
	VirtualMemory func() (*mem.VirtualMemoryStat, error)
}

func NewCollector() *Collector {
	return &Collector{ProcRoot: "/proc", SysRoot: "/sys", VirtualMemory: mem.VirtualMemory}
}

func (c *Collector) Read() Snapshot {
	start := time.Now()
	s := Snapshot{CapturedAt: start.UTC(), PageSizeBytes: os.Getpagesize(), Errors: make(map[string]string)}
	for name, id := range map[string]int32{"monotonic": unix.CLOCK_MONOTONIC, "boottime": unix.CLOCK_BOOTTIME} {
		var ts unix.Timespec
		if err := unix.ClockGettime(id, &ts); err != nil {
			s.Errors[name] = err.Error()
			continue
		}
		ns := ts.Nano()
		if name == "monotonic" {
			s.MonotonicNS = &ns
		} else {
			s.BoottimeNS = &ns
		}
	}
	v, err := c.VirtualMemory()
	if err != nil {
		s.Errors["ram"] = err.Error()
	} else if v == nil || v.Total == 0 || v.Free > v.Total || v.Available > v.Total || v.Used > v.Total || v.Cached < v.SReclaimable || math.IsNaN(v.UsedPercent) || math.IsInf(v.UsedPercent, 0) {
		s.Errors["ram"] = "invalid or unavailable RAM statistics"
	} else {
		s.RAM = &RAM{Total: v.Total, Free: v.Free, Available: v.Available, Used: v.Used, UsedPercent: v.UsedPercent, Buffers: v.Buffers, Cached: v.Cached - v.SReclaimable, SReclaimable: v.SReclaimable, SUnreclaim: v.SUnreclaim, Dirty: v.Dirty, Writeback: v.Writeback, Shmem: v.Shared}
		if v.SwapFree > v.SwapTotal {
			s.Errors["swap"] = "swap free exceeds total"
		} else {
			s.Swap = &Swap{Total: v.SwapTotal, Free: v.SwapFree, Used: v.SwapTotal - v.SwapFree}
			if s.Swap.Total > 0 {
				s.Swap.UsedPercent = float64(s.Swap.Used) / float64(s.Swap.Total) * 100
			}
		}
	}
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(c.ProcRoot, name)) }
	for _, name := range []string{"memory", "io"} {
		data, e := read("pressure/" + name)
		var p *PSI
		if e == nil {
			p, e = parsePSI(string(data))
		}
		if e != nil {
			s.Errors[name+"_psi"] = e.Error()
		}
		if name == "memory" {
			s.MemoryPSI = p
		} else {
			s.IOPSI = p
		}
	}
	data, err := read("vmstat")
	if err == nil {
		s.VMStat, err = parseVMStat(string(data))
	}
	if err != nil {
		s.Errors["vmstat"] = err.Error()
	} else {
		for _, key := range []string{"pswpin", "pswpout", "pgmajfault", "pgscan_direct", "pgsteal_direct"} {
			if _, ok := s.VMStat[key]; !ok {
				s.Errors["vmstat/"+key] = "counter unavailable"
			}
		}
		hasAllocstall := false
		for key := range s.VMStat {
			if strings.HasPrefix(key, "allocstall") {
				hasAllocstall = true
			}
		}
		if !hasAllocstall {
			s.Errors["vmstat/allocstall"] = "counters unavailable"
		}
	}
	data, err = read("swaps")
	if err == nil {
		s.Devices, err = parseSwaps(string(data))
	}
	if err != nil {
		s.Errors["swap_devices"] = err.Error()
	} else {
		s.ZRAM = make(map[string]ZRAM)
		for _, d := range *s.Devices {
			name := filepath.Base(d.Name)
			if !isZRAM(name) {
				continue
			}
			b, e := os.ReadFile(filepath.Join(c.SysRoot, "block", name, "mm_stat"))
			var z ZRAM
			if e == nil {
				z, e = parseZRAM(string(b))
			}
			if e != nil {
				s.Errors["zram/"+name] = e.Error()
			} else {
				s.ZRAM[d.Name] = z
			}
		}
	}
	s.CollectionDurationNS = time.Since(start).Nanoseconds()
	return s
}

func isZRAM(name string) bool {
	if !strings.HasPrefix(name, "zram") || len(name) == 4 {
		return false
	}
	for _, r := range name[4:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parsePSI(data string) (*PSI, error) {
	values := make(map[string]Pressure)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || (f[0] != "some" && f[0] != "full") {
			continue
		}
		fields := make(map[string]string)
		for _, item := range f[1:] {
			k, v, ok := strings.Cut(item, "=")
			if !ok {
				return nil, fmt.Errorf("invalid PSI field %q", item)
			}
			fields[k] = v
		}
		var p Pressure
		for key, target := range map[string]*float64{"avg10": &p.Avg10, "avg60": &p.Avg60, "avg300": &p.Avg300} {
			v, err := strconv.ParseFloat(fields[key], 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
				return nil, fmt.Errorf("invalid PSI %s", key)
			}
			*target = v
		}
		var err error
		p.Total, err = strconv.ParseUint(fields["total"], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid PSI total: %w", err)
		}
		values[f[0]] = p
	}
	some, a := values["some"]
	full, b := values["full"]
	if !a || !b {
		return nil, fmt.Errorf("PSI requires some and full")
	}
	return &PSI{Some: some, Full: full}, nil
}

func parseVMStat(data string) (map[string]uint64, error) {
	result := make(map[string]uint64)
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		key := f[0]
		if key != "pswpin" && key != "pswpout" && key != "pgmajfault" && key != "pgscan_direct" && key != "pgsteal_direct" && !strings.HasPrefix(key, "allocstall") {
			continue
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("invalid vmstat %s", key)
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid vmstat %s: %w", key, err)
		}
		result[key] = v
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no supported vmstat counters")
	}
	return result, nil
}

func parseSwaps(data string) (*[]SwapDevice, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "Filename") {
		return nil, fmt.Errorf("missing swaps header")
	}
	result := make([]SwapDevice, 0)
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) != 5 {
			return nil, fmt.Errorf("invalid swaps row")
		}
		size, e1 := strconv.ParseUint(f[2], 10, 64)
		used, e2 := strconv.ParseUint(f[3], 10, 64)
		priority, e3 := strconv.Atoi(f[4])
		if e1 != nil || e2 != nil || e3 != nil || size > math.MaxUint64/1024 || used > size {
			return nil, fmt.Errorf("invalid swaps values")
		}
		result = append(result, SwapDevice{Name: f[0], Type: f[1], Size: size * 1024, Used: used * 1024, Priority: priority})
	}
	return &result, nil
}

func parseZRAM(data string) (ZRAM, error) {
	f := strings.Fields(data)
	if len(f) < 3 {
		return ZRAM{}, fmt.Errorf("short zram mm_stat")
	}
	var z ZRAM
	for i, p := range []*uint64{&z.Original, &z.Compressed, &z.MemoryUsed} {
		v, err := strconv.ParseUint(f[i], 10, 64)
		if err != nil {
			return ZRAM{}, err
		}
		*p = v
	}
	return z, nil
}
