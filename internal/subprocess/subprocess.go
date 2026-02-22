package subprocess

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buildbuddy-io/reninja/internal/exit_status"
)

var (
	once        sync.Once
	interrupted atomic.Bool
)

func handleSignals() {
	once.Do(func() {
		signalChannel := make(chan os.Signal, 1)
		signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-signalChannel
			interrupted.Store(true)
		}()
	})
}

// SetupSignalHandling should be called at the start of main() to ensure
// signals are caught before any subprocesses are started.
func SetupSignalHandling() {
	handleSignals()
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
	// wakeup is signaled (non-blocking) when any subprocess completes.
	// This allows DoWork to block efficiently instead of busy-polling,
	// matching the C++ ppoll/select behavior.
	wakeup chan struct{}
}

func NewSet() *Set {
	return &Set{
		mu:       &sync.Mutex{},
		running:  make([]*Subprocess, 0),
		finished: make([]*Subprocess, 0),
		wakeup:   make(chan struct{}, 1),
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

	go func() {
		<-sub.done

		// Move this subprocess from running to finished under the lock.
		// Because each goroutine appends to finished as it completes,
		// the finished slice preserves completion order (not start order).
		// This matches the C++ ppoll/pselect behavior where events are
		// delivered per-fd as they become ready.
		s.mu.Lock()
		s.running = slices.DeleteFunc(s.running, func(r *Subprocess) bool { return r == sub })
		s.finished = append(s.finished, sub)
		s.mu.Unlock()

		select {
		case s.wakeup <- struct{}{}:
		default:
		}
	}()
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

// DoWork blocks until at least one subprocess has completed.
//
// Subprocesses move themselves from running to finished in completion
// order (see Add), matching the C++ ninja behavior where ppoll/pselect
// delivers events per-fd as they become ready.
func (s *Set) DoWork() bool {
	if interrupted.Load() {
		return true
	}

	s.mu.Lock()
	hasFinished := len(s.finished) > 0
	hasRunning := len(s.running) > 0
	s.mu.Unlock()

	// If any subprocesses have already completed, return immediately.
	if hasFinished || !hasRunning {
		return interrupted.Load()
	}

	// No subprocess is done yet. Block until one completes or we're
	// interrupted.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for woken := false; !woken; {
		select {
		case <-s.wakeup:
			// A subprocess completed.
			woken = true
		case <-ticker.C:
			if interrupted.Load() {
				return true
			}
		}
	}

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
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range s.running {
		sub.Kill()
	}
	s.running = s.running[:0]
}
