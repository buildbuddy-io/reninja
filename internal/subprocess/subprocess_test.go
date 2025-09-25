package subprocess_test

import (
	"fmt"
	"runtime"
	"syscall"
	"testing"

	"github.com/buildbuddy-io/gin/internal/subprocess"
	"github.com/buildbuddy-io/gin/internal/exit_status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBadCommandStderr(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("cmd /c ninja_no_such_command", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		subprocs.DoWork()
	}
	subprocs.DoWork()

	assert.NotEqual(t, exit_status.ExitSuccess, subproc.Finish())
}

func TestNoSuchCommand(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("ninja_no_such_command", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		subprocs.DoWork()
	}
	subprocs.DoWork()

	assert.NotEqual(t, exit_status.ExitSuccess, subproc.Finish())
}

func TestInterruptChild(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -INT $$", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		subprocs.DoWork()
	}
	subprocs.DoWork()

	assert.Equal(t, exit_status.ExitStatusType(130), subproc.Finish())	
}

func TestInterruptParent(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -INT $PPID ; sleep 1", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		interrupted := subprocs.DoWork()
		if (interrupted) {
			return
		}
	}

	assert.False(t, true, "should have been interrupted")
}

func TestInterruptChildWithSigterm(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -TERM $$", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		subprocs.DoWork()
	}
	subprocs.DoWork()

	assert.Equal(t, exit_status.ExitStatusType(130), subproc.Finish())	
}

func TestInterruptParentWithSigterm(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -TERM $PPID ; sleep 1", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		interrupted := subprocs.DoWork()
		if (interrupted) {
			return
		}
	}

	assert.False(t, true, "should have been interrupted")
}

func TestInterruptChildWithSighup(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -HUP $$", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		subprocs.DoWork()
	}
	subprocs.DoWork()

	assert.Equal(t, exit_status.ExitStatusType(130), subproc.Finish())	
}

func TestInterruptParentWithSighup(t *testing.T) {
	subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("kill -HUP $PPID ; sleep 1", false /*=useConsole*/)
	require.NoError(t, err)
	require.NotNil(t, subproc)

	for !subproc.Done() {
		interrupted := subprocs.DoWork()
		if (interrupted) {
			return
		}
	}

	assert.False(t, true, "should have been interrupted")
}

func getSimpleCommand() string {
	if runtime.GOOS == "windows" {
		return "cmd /c dir \\"
	} else {
		return "ls /"
	}
}

func TestSetWithSingle(t *testing.T) {
        subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add(getSimpleCommand(), false /*=useConsole*/)
        require.NoError(t, err)
        require.NotNil(t, subproc)

        for !subproc.Done() {
                subprocs.DoWork()
        }
	subprocs.DoWork()

        assert.Equal(t, exit_status.ExitStatusType(0), subproc.Finish())
	assert.NotEqual(t, "", subproc.GetOutput())
}

func TestSetWithMulti(t *testing.T) {
	subprocs := subprocess.NewSet()
	
	processes := make([]*subprocess.Subprocess, 3)
	commands := make([]string, 3)
	commands[0] = getSimpleCommand()
	if runtime.GOOS == "windows" {
		commands[1] = "cmd /c echo hi"
		commands[2] = "cmd /c time /t"
	} else {
		commands[1] = "id -u"
		commands[2] = "pwd"
	}

	var err error
	for i := range 3 {
		processes[i], err = subprocs.Add(commands[i], false /*=useConsole*/)
		require.NoError(t, err)
		require.NotNil(t, processes[i])
	}

	require.Equal(t, 3, len(subprocs.Running()))

	for i := range 3 {
		require.False(t, processes[i].Done())
		require.Equal(t, "", processes[i].GetOutput())
	}

	for !processes[0].Done() || !processes[1].Done() || !processes[2].Done() {
		require.Greater(t, len(subprocs.Running()), 0)
		subprocs.DoWork()
	}
	subprocs.DoWork() // required b/c subproc can be Done and doWork not know it yet.

	require.Equal(t, 0, len(subprocs.Running()))
	require.Equal(t, 3, len(subprocs.Finished()))
	
	for i := range 3 {
		assert.Equal(t, exit_status.ExitStatusType(0), processes[i].Finish())
		assert.NotEqual(t, "", processes[i].GetOutput())
	}
}

func TestSetWithLots(t *testing.T) {
	maxProcs := 1025
	subprocs := subprocess.NewSet()

	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err == nil {
		if int(rLimit.Cur) < maxProcs {
			fmt.Printf("Raise [ulimit -n] above %d (currently %d) to make this test go\n", maxProcs, rLimit.Cur)
			return
		}
	}
	
	processes := make([]*subprocess.Subprocess, 0)
	for range maxProcs {
		subproc, err := subprocs.Add("/bin/echo", false /*=useConsole*/)
		require.NoError(t, err)
		require.NotNil(t, subproc)
		processes = append(processes, subproc)
	}

	for len(subprocs.Running()) > 0 {
		subprocs.DoWork()
	}

	for i := range maxProcs {	
		assert.Equal(t, exit_status.ExitStatusType(0), processes[i].Finish())
		assert.NotEqual(t, "", processes[i].GetOutput())
	}
	
	require.Equal(t, maxProcs, len(subprocs.Finished()))
}

func TestReadStdin(t *testing.T) {
        subprocs := subprocess.NewSet()
	subproc, err := subprocs.Add("cat -", false /*=useConsole*/)
	require.NoError(t, err)
	
        for !subproc.Done() {
                subprocs.DoWork()
        }
	subprocs.DoWork()

        assert.Equal(t, exit_status.ExitStatusType(0), subproc.Finish())
	require.Equal(t, 1, len(subprocs.Finished()))
}
