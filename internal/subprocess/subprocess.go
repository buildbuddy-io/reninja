package subprocess

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/buildbuddy-io/reninja/internal/exit_status"
)

var (
	once        sync.Once
	interrupted atomic.Bool
)

func handleSignals() {
	once.Do(func() {
		signalChannel := make(chan os.Signal, 1)
		signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-signalChannel
			interrupted.Store(true)
		}()
	})
}

func Interrupted() bool {
	return interrupted.Load()
}

type Subprocess struct {
	ctx        context.Context
	cancelFunc context.CancelFunc

	cmd          *exec.Cmd
	stdOutAndErr *bytes.Buffer

	mu        *sync.Mutex
	exitError *exec.ExitError
	done      chan struct{}
}

func NewSubprocess(command string, useConsole bool) (*Subprocess, error) {
	handleSignals()

	ctx, cancelFunc := context.WithCancel(context.TODO())
	s := &Subprocess{
		ctx:        ctx,
		cancelFunc: cancelFunc,
		cmd:        exec.CommandContext(ctx, "/bin/sh", "-c", command),
		done:       make(chan struct{}),
		mu:         &sync.Mutex{},
	}

	if useConsole {
		s.cmd.Stdout = os.Stdout
		s.cmd.Stderr = os.Stdout
	} else {
		s.stdOutAndErr = &bytes.Buffer{}
		s.cmd.Stdout = s.stdOutAndErr
		s.cmd.Stderr = s.stdOutAndErr
	}

	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		if err := s.cmd.Wait(); err != nil {
			s.mu.Lock()
			s.exitError = err.(*exec.ExitError)
			s.mu.Unlock()
		}
		close(s.done)
	}()
	return s, nil
}

func (s *Subprocess) Kill() {
	s.cancelFunc()
}

func (s *Subprocess) GetOutput() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdOutAndErr == nil {
		return ""
	}
	return string(s.stdOutAndErr.Bytes())
}

func (s *Subprocess) TryFinish(wait bool) bool {
	if wait {
		select {
		case <-s.done:
			return true
		}
	} else {
		select {
		case <-s.done:
			return true
		default:
			return false
		}
	}
}

// Returns ExitSuccess on successful process exit, ExitInterrupted if
// the process was interrupted, ExitFailure if it otherwise failed.
func (s *Subprocess) Finish() exit_status.ExitStatusType {
	s.TryFinish(true /*=wait*/)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exitError == nil {
		return exit_status.ExitSuccess
	} else if strings.Contains(s.exitError.String(), "interrupt") {
		return exit_status.ExitInterrupted
	} else if strings.Contains(s.exitError.String(), "terminated") {
		return exit_status.ExitInterrupted
	} else if strings.Contains(s.exitError.String(), "hangup") {
		return exit_status.ExitInterrupted
	} else {
		return exit_status.ExitStatusType(s.exitError.ProcessState.ExitCode())
	}
}

func (s *Subprocess) Done() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

type Set struct {
	mu       *sync.Mutex
	running  []*Subprocess
	finished []*Subprocess
}

func NewSet() *Set {
	return &Set{
		mu:       &sync.Mutex{},
		running:  make([]*Subprocess, 0),
		finished: make([]*Subprocess, 0),
	}
}

func (s *Set) Add(command string, useConsole bool) (*Subprocess, error) {
	sub, err := NewSubprocess(command, useConsole)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.running = append(s.running, sub)
	s.mu.Unlock()
	return sub, nil
}

func (s *Set) Running() []*Subprocess {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := make([]*Subprocess, len(s.running))
	copy(r, s.running)
	return r
}

func (s *Set) Finished() []*Subprocess {
	s.mu.Lock()
	defer s.mu.Unlock()

	f := make([]*Subprocess, len(s.finished))
	copy(f, s.finished)
	return f
}

func (s *Set) DoWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	nextRunning := s.running[:0]
	for _, sub := range s.running {
		if interrupted.Load() {
			return true
		}
		if sub.Done() {
			s.finished = append(s.finished, sub)
		} else {
			nextRunning = append(nextRunning, sub)
		}
	}
	s.running = nextRunning
	return interrupted.Load()
}

func (s *Set) NextFinished() *Subprocess {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.finished) == 0 {
		return nil
	}
	sub := s.finished[0]
	s.finished = s.finished[1:]
	return sub
}

func (s *Set) Clear() {
	for _, sub := range s.running {
		sub.Kill()
	}
	s.running = s.running[:0]
}
