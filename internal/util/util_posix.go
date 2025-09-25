package util

import (
	"syscall"
)

func GetLoadAverage() float64 {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return -1
	}
	const siLoadShift = 16
	return float64(info.Loads[0]) / float64(1<<siLoadShift)
}

