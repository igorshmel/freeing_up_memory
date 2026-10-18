package memory

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/mem"
)

const psiFixture = "some avg10=1.25 avg60=0.50 avg300=0.10 total=123456\nfull avg10=0.25 avg60=0.10 avg300=0.00 total=1234\n"

func TestPSI(t *testing.T) {
	p, err := parsePSI(psiFixture)
	if err != nil || p.Some.Total != 123456 || p.Full.Avg10 != .25 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "some avg10=0 avg60=0 avg300=0 total=0", strings.Replace(psiFixture, "1.25", "NaN", 1), strings.Replace(psiFixture, "123456", "-1", 1), strings.Replace(psiFixture, "1.25", "101", 1), strings.Replace(psiFixture, "avg60=0.50 ", "", 1)} {
		if p, err := parsePSI(bad); err == nil || p != nil {
			t.Errorf("accepted malformed PSI: %q", bad)
		}
	}
}

func TestSwapAndZRAMParsers(t *testing.T) {
	header := "Filename\tType\tSize\tUsed\tPriority\n"
	d, err := parseSwaps(header + "/swapfile file 8192 0 -2\n/dev/zram0 partition 4096 1024 100\n")
	if err != nil || len(*d) != 2 || (*d)[1].Used != 1024*1024 || (*d)[0].Priority != -2 {
		t.Fatalf("%v %v", d, err)
	}
	d, err = parseSwaps(header)
	if err != nil || len(*d) != 0 {
		t.Fatalf("no swap: %v %v", d, err)
	}
	for _, bad := range []string{"", header + "/swapfile file 10 11 -2", header + "/swapfile file 10 -1 -2", header + "/swapfile file 18446744073709551615 0 -2"} {
		if _, err := parseSwaps(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	z, err := parseZRAM("1000 200 256 0 300 4 5 6 7 extra")
	if err != nil || z.Original != 1000 || z.Compressed != 200 || z.MemoryUsed != 256 {
		t.Fatalf("%+v %v", z, err)
	}
	for _, bad := range []string{"", "1 2", "1 -2 3"} {
		if _, err := parseZRAM(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestVMStatRawUnits(t *testing.T) {
	v, err := parseVMStat("pswpin 5\npswpout 6\npgmajfault 7\nallocstall_normal 8\npgscan_direct 9\npgsteal_direct 10\npgscan_direct_throttle 11\nunrelated 44\n")
	if err != nil || v["pswpin"] != 5 || v["pgmajfault"] != 7 || len(v) != 6 {
		t.Fatalf("%v %v", v, err)
	}
	for _, bad := range []string{"", "pswpin no", "allocstall_normal -1", "pgscan_direct"} {
		if _, err := parseVMStat(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCollectorOptionalFailuresAndCacheAccounting(t *testing.T) {
	root := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/pressure/memory", psiFixture)
	write("proc/pressure/io", psiFixture)
	write("proc/swaps", "Filename Type Size Used Priority\n/dev/zram0 partition 4096 1024 100\n")
	write("proc/vmstat", "pswpin 123\npswpout 45\npgmajfault 6\npgscan_direct 7\npgsteal_direct 8\nallocstall_normal 9\n")
	write("sys/block/zram0/mm_stat", "1048576 200000 220000 0 0")
	vm := &mem.VirtualMemoryStat{Total: 16000, Free: 1000, Available: 5000, Used: 11000, UsedPercent: 68.75, Cached: 3000, SReclaimable: 1000, SwapTotal: 8000, SwapFree: 6000}
	c := &Collector{ProcRoot: filepath.Join(root, "proc"), SysRoot: filepath.Join(root, "sys"), VirtualMemory: func() (*mem.VirtualMemoryStat, error) { return vm, nil }}
	s := c.Read()
	if len(s.Errors) != 0 || s.RAM.Cached != 2000 || s.Swap.Used != 2000 || s.Swap.UsedPercent != 25 || s.ZRAM["/dev/zram0"].MemoryUsed != 220000 {
		t.Fatalf("%+v", s)
	}
	if s.MonotonicNS == nil || s.BoottimeNS == nil || s.CollectionDurationNS < 0 {
		t.Fatal("missing timing")
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "proc/pressure/io")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "sys/block/zram0/mm_stat")); err != nil {
		t.Fatal(err)
	}
	s = c.Read()
	if s.RAM == nil || s.IOPSI != nil || s.Errors["io_psi"] == "" || s.Errors["zram/zram0"] == "" {
		t.Fatalf("optional failures: %+v", s)
	}
	vm.SwapTotal = 0
	vm.SwapFree = 0
	if s = c.Read(); s.Swap.UsedPercent != 0 {
		t.Fatal("zero swap percentage")
	}
	c.VirtualMemory = func() (*mem.VirtualMemoryStat, error) { return nil, errors.New("RAM unavailable") }
	s = c.Read()
	if s.RAM != nil || s.Errors["ram"] == "" {
		t.Fatal("RAM error not surfaced")
	}
	c.VirtualMemory = func() (*mem.VirtualMemoryStat, error) { return &mem.VirtualMemoryStat{}, nil }
	if c.Read().RAM != nil {
		t.Fatal("gopsutil empty result accepted")
	}
}

func TestLimitedStderr(t *testing.T) {
	b := &limitedBuffer{limit: 8 * 1024}
	p := strings.Repeat("x", 10*1024)
	for i := 0; i < 2; i++ {
		n, err := b.Write([]byte(p))
		if n != len(p) || err != nil {
			t.Fatal(n, err)
		}
	}
	if b.Len() != 8*1024 || !b.truncated {
		t.Fatal("stderr was not bounded")
	}
	// exec copies from an os.File; io.Copy must not bypass Write via ReadFrom.
	b = &limitedBuffer{limit: 8 * 1024}
	reader := struct{ io.Reader }{strings.NewReader(p)}
	if _, err := io.Copy(b, reader); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 8*1024 || !b.truncated {
		t.Fatal("io.Copy bypassed stderr limit")
	}
}
