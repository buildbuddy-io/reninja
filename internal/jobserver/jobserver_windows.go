//go:build windows

package jobserver

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	libKernel32       = windows.NewLazySystemDLL("kernel32.dll")
	pOpenSemaphoreA   uintptr
	pReleaseSemaphore uintptr
)

// This is cobbled together from:
//  1) https://github.com/zzl/go-win32api/
//  2) https://github.com/heucuva/go-win32/
//  3) https://pkg.go.dev/golang.org/x/sys/windows

const (
	WAIT_TIMEOUT          = 0x00000102
	errnoERROR_IO_PENDING = 997
)

var (
	errERROR_IO_PENDING error = syscall.Errno(errnoERROR_IO_PENDING)
	errERROR_EINVAL     error = syscall.EINVAL
	WaitTimeout               = errors.New("wait timeout")
)

type SYNCHRONIZATION_ACCESS_RIGHTS uint32

const (
	SEMAPHORE_MODIFY_STATE      SYNCHRONIZATION_ACCESS_RIGHTS = 2
	SYNCHRONIZATION_SYNCHRONIZE SYNCHRONIZATION_ACCESS_RIGHTS = 1048576
)

// errnoErr returns common boxed Errno values, to prevent
// allocations at runtime.
func errnoErr(e syscall.Errno) error {
	switch e {
	case 0:
		return errERROR_EINVAL
	case errnoERROR_IO_PENDING:
		return errERROR_IO_PENDING
	}
	// TODO: add more here, after collecting data on the common
	// error values see on Windows. (perhaps when running
	// all.bat?)
	return e
}

func LazyAddr(pAddr *uintptr, lib *windows.LazyDLL, procName string) uintptr {
	addr := atomic.LoadUintptr(pAddr)
	if addr == 0 {
		addr = lib.NewProc(procName).Addr()
		atomic.StoreUintptr(pAddr, addr)
	}
	return addr
}

func OpenSemaphoreA(dwDesiredAccess *uint32, bInheritHandle bool, lpName string) (handle windows.Handle, err error) {
	var _p2 uint32
	if bInheritHandle {
		_p2 = 1
	}
	var _p3 *byte
	if _p3, err = syscall.BytePtrFromString(lpName); err != nil {
		return
	}

	addr := LazyAddr(&pOpenSemaphoreA, libKernel32, "OpenSemaphoreA")
	r1, _, e1 := syscall.SyscallN(addr, uintptr(unsafe.Pointer(dwDesiredAccess)), uintptr(_p2), uintptr(unsafe.Pointer(_p3)))
	handle = windows.Handle(r1)
	if r1 == 0 {
		err = errnoErr(e1)
	}
	return
}

func ReleaseSemaphore(hSemaphore windows.Handle, lReleaseCount int32, lpPreviousCount *int32) (b bool, err error) {
	h := atomic.LoadUintptr((*uintptr)(&handle))
	addr := LazyAddr(&pReleaseSemaphore, libKernel32, "ReleaseSemaphore")
	r1, _, e1 := syscall.SyscallN(addr, uintptr(handle), uintptr(lReleaseCount), uintptr(unsafe.Pointer(lpPreviousCount)))
	b = r1
	if r1 == 0 {
		err = errnoErr(e1)
	}
	return
}

func WaitForSingleObject(handle windows.Handle, durationMillis uint32) error {
	h := atomic.LoadUintptr((*uintptr)(&handle))
	s, e := windows.WaitForSingleObject(windows.Handle(h), durationMillis)
	switch s {
	case windows.WAIT_OBJECT_0:
		return nil
	case WAIT_TIMEOUT:
		return WaitTimeout
	default:
		return os.NewSyscallError("WaitForSingleObject", e)
	}
}

type windowsClient struct {
	handle          windows.Handle
	hasImplicitSlot bool
}

func (c *windowsClient) TryAcquire() Slot {
	if c.IsValid() {
		if c.hasImplicitSlot {
			c.hasImplicitSlot = false
			return CreateImplicitSlot()
		}

		if WaitForSingleObject(c.handle, 0) == nil {
			// Hard-code value 1 for the explicit slot value.
			return CreateExplicitSlot(1)
		}
	}
	return NewSlot()
}

func (c *unixClient) Release(slot Slot) {
	if !slot.Valid() {
		return
	}
	if slot.Implicit() {
		if !c.hasImplicitSlot {
			panic("Implicit slot cannot be released twice!")
		}
		c.hasImplicitSlot = true
		return
	}
	ReleaseSemaphore(c.handle, 1)
}

func (c *windowsClient) InitWithSemaphore(name string) error {
	flags := uint32(SYNCHRONIZATION_SYNCHRONIZE | SEMAPHORE_MODIFY_STATE)
	h, err := OpenSemaphoreA(&flags, false, name)
	if err != nil {
		return err
	}
	c.handle = h

	runtime.AddCleanup(c, func(h windows.Handle) {
		syscall.CloseHandle(syscall.Handle(h))
	}, h)
	return nil
}

func (c *windowsClient) IsValid() bool {
	return c.handle != 0
}

func (c *windowsClient) Create(config *Config) error {
	if config.Mode == ModePosixFifo {
		return c.InitWithSemaphore(config.Path)
	} else {
		return fmt.Errorf("Unsupported jobserver mode")
	}
}

func NewClient() Client {
	return &windowsClient{}
}
