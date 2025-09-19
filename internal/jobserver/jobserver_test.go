package jobserver_test

import (
	"runtime"
	"testing"

	"github.com/buildbuddy-io/gin/internal/jobserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobserverSlotTest(t *testing.T) {
	// Default construction.
	slot := jobserver.NewSlot()
	require.False(t, slot.Valid())

	// Construct implicit slot
	slot0 := jobserver.CreateImplicitSlot()
	assert.True(t, slot0.Valid())
	assert.True(t, slot0.Implicit())
	assert.False(t, slot0.Explicit())

	// Construct explicit slots
	slot1 := jobserver.CreateExplicitSlot(10)
	assert.True(t, slot1.Valid())
	assert.False(t, slot1.Implicit())
	assert.True(t, slot1.Explicit())
	assert.Equal(t, uint8(10), slot1.ExplicitValue())

	slot2 := jobserver.CreateExplicitSlot(42)
	assert.True(t, slot2.Valid())
	assert.False(t, slot2.Implicit())
	assert.True(t, slot2.Explicit())
	assert.Equal(t, uint8(42), slot2.ExplicitValue())

	// Note: Go doesn't have move semantics like C++, so we skip the move tests
	// Instead verify that slots maintain their values
	slot2 = slot1
	assert.True(t, slot2.Valid())
	assert.True(t, slot2.Explicit())
	assert.Equal(t, uint8(10), slot2.ExplicitValue())

	slot1 = slot0
	assert.True(t, slot1.Valid())
	assert.True(t, slot1.Implicit())
	assert.False(t, slot1.Explicit())
}

func TestParseMakeFlagsValue(t *testing.T) {
	js := &jobserver.Jobserver{}

	// Passing empty string does not crash
	config, err := js.ParseMakeFlagsValue("")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	// Passing a string that only contains whitespace does not crash
	config, err = js.ParseMakeFlagsValue("  \t")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	// Passing an `n` in the first word reports no mode
	config, err = js.ParseMakeFlagsValue("kns --jobserver-auth=fifo:foo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	// Passing "--jobserver-auth=fifo:<path>" works
	config, err = js.ParseMakeFlagsValue("--jobserver-auth=fifo:foo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePosixFifo, config.Mode)
	assert.Equal(t, "foo", config.Path)

	// Passing an initial " -j" or " -j<count>" works
	config, err = js.ParseMakeFlagsValue(" -j --jobserver-auth=fifo:foo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePosixFifo, config.Mode)
	assert.Equal(t, "foo", config.Path)

	// Passing an initial " -j<count>" works
	config, err = js.ParseMakeFlagsValue(" -j10 --jobserver-auth=fifo:foo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePosixFifo, config.Mode)
	assert.Equal(t, "foo", config.Path)

	// Passing an `n` in the first word _after_ a dash works though, i.e.
	// It is not interpreted as GNU Make dry-run flag.
	config, err = js.ParseMakeFlagsValue("-one-flag --jobserver-auth=fifo:foo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePosixFifo, config.Mode)

	config, err = js.ParseMakeFlagsValue("--jobserver-auth=semaphore_name")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeWin32Semaphore, config.Mode)
	assert.Equal(t, "semaphore_name", config.Path)

	config, err = js.ParseMakeFlagsValue("--jobserver-auth=10,42")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePipe, config.Mode)

	config, err = js.ParseMakeFlagsValue("--jobserver-auth=-1,42")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	config, err = js.ParseMakeFlagsValue("--jobserver-auth=10,-42")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModeNone, config.Mode)

	config, err = js.ParseMakeFlagsValue(
		"--jobserver-auth=10,42 --jobserver-fds=12,44 " +
			"--jobserver-auth=fifo:/tmp/fifo")
	require.NoError(t, err)
	assert.Equal(t, jobserver.ModePosixFifo, config.Mode)
	assert.Equal(t, "/tmp/fifo", config.Path)

	config, err = js.ParseMakeFlagsValue("--jobserver-fds=10,")
	require.Error(t, err)
	assert.Equal(t, "Invalid file descriptor pair [\"10,\"]", err.Error())
}

func TestParseNativeMakeFlagsValue(t *testing.T) {
	js := &jobserver.Jobserver{}

	// --jobserver-auth=R,W is not supported
	config, err := js.ParseNativeMakeFlagsValue("--jobserver-auth=3,4")
	require.Error(t, err)
	assert.Nil(t, config)
	assert.Equal(t, "Pipe-based protocol is not supported!", err.Error())

	if runtime.GOOS == "windows" {
		// --jobserver-auth=NAME works on Windows
		config, err = js.ParseNativeMakeFlagsValue("--jobserver-auth=semaphore_name")
		require.NoError(t, err)
		assert.Equal(t, jobserver.ModeWin32Semaphore, config.Mode)
		assert.Equal(t, "semaphore_name", config.Path)

		// --jobserver-auth=fifo:PATH does not work on Windows
		config, err = js.ParseNativeMakeFlagsValue("--jobserver-auth=fifo:foo")
		require.Error(t, err)
		assert.Equal(t, "FIFO mode is not supported on Windows!", err.Error())
	} else {
		// --jobserver-auth=NAME does not work on Posix
		config, err = js.ParseNativeMakeFlagsValue("--jobserver-auth=semaphore_name")
		require.Error(t, err)
		assert.Equal(t, "Semaphore mode is not supported on Posix!", err.Error())

		// --jobserver-auth=fifo:PATH works on Posix
		config, err = js.ParseNativeMakeFlagsValue("--jobserver-auth=fifo:foo")
		require.NoError(t, err)
		assert.Equal(t, jobserver.ModePosixFifo, config.Mode)
		assert.Equal(t, "foo", config.Path)
	}
}
