package memory

import (
	"fmt"
	"github.com/shirou/gopsutil/mem"
	"os/exec"
)

const Threshold = 1.0 * 1024 * 1024 * 1024 // 1 GB

func GetFreeMemory() (uint64, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}
	return v.Free, nil
}

func ClearCache() error {
	cmd := exec.Command("sh", "-c", "sync && echo 3 | sudo tee /proc/sys/vm/drop_caches")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to clear cache: %v", err)
	}
	return nil
}
