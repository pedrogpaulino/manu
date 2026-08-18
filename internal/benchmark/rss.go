package benchmark

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// linuxMaxRSS reads the kernel high-water mark when the Linux proc filesystem
// is available. No cross-platform estimate is substituted when it is absent.
func linuxMaxRSS() (uint64, string) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		return 0, "unavailable"
	}
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, "unavailable"
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, "unavailable"
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, "unavailable"
		}
		// Linux reports VmHWM in KiB. This unit conversion is part of the
		// method, not an estimate of memory unavailable from the kernel.
		return value * 1024, "linux_proc_vm_hwm_bytes"
	}
	return 0, "unavailable"
}
