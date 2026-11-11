//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess     = kernel32.NewProc("OpenProcess")
	procCloseHandle     = kernel32.NewProc("CloseHandle")
	procGetProcessTimes = kernel32.NewProc("GetProcessTimes")
	psapi               = syscall.NewLazyDLL("psapi.dll")
	procMemoryInfo      = psapi.NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters is PROCESS_MEMORY_COUNTERS (psapi.h). WorkingSetSize is the resident set.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// procStats reads the resident set (bytes) and the CPU time (kernel + user, seconds) of pid through the Win32 API.
func procStats(pid int) (rss int64, cpuSeconds float64, err error) {
	const access = 0x1000 | 0x0400 | 0x0010 // PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_QUERY_INFORMATION | PROCESS_VM_READ
	h, _, e := procOpenProcess.Call(access, 0, uintptr(pid))
	if h == 0 {
		return 0, 0, fmt.Errorf("OpenProcess %d: %v", pid, e)
	}
	defer procCloseHandle.Call(h)
	var mc processMemoryCounters
	mc.CB = uint32(unsafe.Sizeof(mc))
	if r, _, e := procMemoryInfo.Call(h, uintptr(unsafe.Pointer(&mc)), uintptr(mc.CB)); r == 0 {
		return 0, 0, fmt.Errorf("GetProcessMemoryInfo: %v", e)
	}
	var creation, exit, kernel, user syscall.Filetime
	if r, _, e := procGetProcessTimes.Call(h, uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r == 0 {
		return 0, 0, fmt.Errorf("GetProcessTimes: %v", e)
	}
	ticks := func(f syscall.Filetime) float64 {
		return float64(uint64(f.HighDateTime)<<32|uint64(f.LowDateTime)) / 1e7
	}
	return int64(mc.WorkingSetSize), ticks(kernel) + ticks(user), nil
}
