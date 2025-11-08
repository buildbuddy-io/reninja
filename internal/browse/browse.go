package browse

import (
	"bytes"
	"fmt"
	"github.com/buildbuddy-io/gin/internal/state"
	"io"
	"os"
	"os/exec"

	_ "embed"
)

//go:embed browse.py
var browsePy []byte

func RunBrowsePython(state *state.State, ninjaCommand, inputFile string, args []string) error {
	pythonCmd := "python"
	if ninjaPython := os.Getenv("NINJA_PYTHON"); ninjaPython != "" {
		pythonCmd = ninjaPython
	}

	cmdArgs := []string{"-"} // we'll pipe the script from stdin.
	cmdArgs = append(cmdArgs, "--ninja-command", ninjaCommand)
	cmdArgs = append(cmdArgs, "-f", inputFile)
	cmdArgs = append(cmdArgs, args...)

	pipeReader, pipeWriter := io.Pipe()

	cmd := exec.Command(pythonCmd, cmdArgs...)
	cmd.Stdin = pipeReader
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		if err == exec.ErrNotFound {
			return fmt.Errorf("%s is required for the browse tool", pythonCmd)
		}
		return fmt.Errorf("failed to start Python: %w", err)
	}

	go func() {
		if len(browsePy) > 0 && browsePy[len(browsePy)-1] == 0 {
			browsePy = browsePy[:len(browsePy)-1]
		}

		// Write the script to the pipe
		io.Copy(pipeWriter, bytes.NewReader(browsePy))
		pipeWriter.Close()
	}()

	return cmd.Wait()
}
