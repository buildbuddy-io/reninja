//go:build darwin && !ios && (amd64 || arm64)

package util

import (
	"syscall"
	"unsafe"
)

type LoadAvg struct {
	LoadAvg [3]uint32
	Scale   int64
}

func GetLoadAverage() float64 {
	data, err := syscall.Sysctl("vm.loadavg")
	if err != nil {
		return 0
	}
	load := (*LoadAvg)(unsafe.Pointer(&[]byte(data)[0]))
	return float64(load.LoadAvg[0]) / float64(load.Scale)
}
