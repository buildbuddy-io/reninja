//go:build ((linux && !android) || (darwin && !ios)) && (amd64 || arm64)

package jobserver_test

import (
	"os"
	"syscall"
	"testing"

	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNullJobserver(t *testing.T) {
	config := jobserver.NewConfig(jobserver.ModeNone)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	client := jobserver.NewClient()
	require.Error(t, client.Create(config))
}

func TestPosixFifoClient(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "ninja_test_jobserver_fifo")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	fifoPath := tempDir + "fifo"
	require.NoError(t, syscall.Mknod(fifoPath, syscall.S_IFIFO|0666, 0))

	slotCount := 5
	writeFd, err := syscall.Open(fifoPath, syscall.O_RDWR, 0)
	require.NoError(t, err)

	for range slotCount {
		slotBytes := []byte{0}
		n, err := syscall.Write(writeFd, slotBytes)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	// Keep the file descriptor opened to ensure the fifo's content
	// persists in kernel memory.

	config := jobserver.NewConfig(jobserver.ModePosixFifo)
	config.Path = fifoPath

	client := jobserver.NewClient()
	require.NoError(t, client.Create(config))

	// Read slots from the pool, and store them
	slots := make([]jobserver.Slot, 0)

	// First slot is always implicit.
	slots = append(slots, client.TryAcquire())
	require.True(t, slots[0].Valid())
	assert.True(t, slots[0].Implicit())

	// Then read kSlotCount slots from the pipe and verify their value.
	for range slotCount {
		slot := client.TryAcquire()
		require.True(t, slot.Valid())
		assert.Equal(t, uint8(0), slot.ExplicitValue())
		slots = append(slots, slot)
	}

	// Pool should be empty now, so next TryAcquire() will fail.
	slot := client.TryAcquire()
	assert.False(t, slot.Valid())
}
