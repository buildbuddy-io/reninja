//go:build ((linux && !android) || (darwin && !ios)) && (amd64 || arm64)
package jobserver

import (
	"fmt"
	"runtime"
	"syscall"
)

type unixClient struct {
	readFD          int
	writeFD         int
	hasImplicitSlot bool
}

func IsFifoDescriptor(fd int) bool {
	stat_t := &syscall.Stat_t{}
	err := syscall.Fstat(fd, stat_t)
	return err == nil && stat_t.Mode&syscall.S_IFMT == syscall.S_IFIFO
}

func (c *unixClient) TryAcquire() Slot {
	if c.hasImplicitSlot {
		c.hasImplicitSlot = false
		return CreateImplicitSlot()
	}

	slotChar := []byte{0}
	for {
		n, err := syscall.Read(c.readFD, slotChar)
		if err != nil && err == syscall.EINTR {
			continue
		}
		if n == 1 {
			return CreateExplicitSlot(uint8(slotChar[0]))
		}
		// shouldn't get here, but some other err or weird read?
		break
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
	slotChar := slot.ExplicitValue()
	for {
		_, err := syscall.Write(c.writeFD, []byte{slotChar})
		if err != nil && err == syscall.EINTR {
			continue
		}
		// shouldn't get here, but nothing to do.
		break
	}
}

type fdPair struct {
	readFD  int
	writeFD int
}

func (c *unixClient) InitWithFifo(fifoPath string) error {
	if fifoPath == "" {
		return fmt.Errorf("Empty fifo path")
	}
	readFD, err := syscall.Open(fifoPath, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("Error opening fifo for reading: %s", err)
	}
	if !IsFifoDescriptor(readFD) {
		syscall.Close(readFD)
		return fmt.Errorf("Not a fifo path: %s", fifoPath)
	}

	writeFD, err := syscall.Open(fifoPath, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0644)
	if err != nil {
		syscall.Close(readFD)
		return fmt.Errorf("Error opening fifo for writing: %s", err)
	}

	c.readFD = readFD
	c.writeFD = writeFD
	c.hasImplicitSlot = true

	runtime.AddCleanup(c, func(p fdPair) {
		if p.readFD >= 0 {
			syscall.Close(p.readFD)
		}
		if p.writeFD >= 0 {
			syscall.Close(p.writeFD)
		}
	}, fdPair{readFD, writeFD})

	return nil
}

func (c *unixClient) Create(config *Config) error {
	if config.Mode == ModePosixFifo {
		return c.InitWithFifo(config.Path)
	} else {
		return fmt.Errorf("Unsupported jobserver mode")
	}
}

func NewClient() Client {
	c := &unixClient{}
	return c
}
