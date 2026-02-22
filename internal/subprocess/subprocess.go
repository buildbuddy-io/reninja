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
	index     int
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
	// completed receives subprocesses as they finish. Buffered so
	// completion goroutines don't block. DoWork drains this channel,
	// sorts each batch by start index, then appends to finished.
	completed    chan *Subprocess
	subprocCount int
}

func NewSet() *Set {
	return &Set{
		mu:        &sync.Mutex{},
		running:   make([]*Subprocess, 0),
		finished:  make([]*Subprocess, 0),
		completed: make(chan *Subprocess, 1024),
	}
}

func (s *Set) Add(command string, useConsole bool) (*Subprocess, error) {
	sub, err := NewSubprocess(command, useConsole)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	sub.index = s.subprocCount
	s.subprocCount++
	s.running = append(s.running, sub)
	s.mu.Unlock()

	go func() {
		<-sub.done
		s.completed <- sub
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

// DoWork blocks until at least one subprocess has completed, then
// moves all simultaneously-completed subprocesses from running to
// finished in start order.
//
// This matches the C++ ninja ppoll/pselect behavior: completions that
// arrive between DoWork calls are batched and sorted by start order
// (the position in running_), while completions separated by DoWork
// calls preserve their natural completion order.
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

	// Block until at least one subprocess completes or we're interrupted.
	var batch []*Subprocess
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for len(batch) == 0 {
		select {
		case sub := <-s.completed:
			batch = append(batch, sub)
		case <-ticker.C:
			if interrupted.Load() {
				return true
			}
		}
	}

	// Drain any other completions that arrived at the same time.
	draining := true
	for draining {
		select {
		case sub := <-s.completed:
			batch = append(batch, sub)
		default:
			draining = false
		}
	}

	// Sort the batch by start order so that simultaneously-completing
	// subprocesses appear in the same order they were started. This
	// matches C++ ninja's ppoll behavior where multiple ready fds are
	// iterated in their position order within the running_ vector.
	slices.SortFunc(batch, func(a, b *Subprocess) int {
		return a.index - b.index
	})

	// Move from running to finished in sorted order.
	s.mu.Lock()
	for _, sub := range batch {
		s.running = slices.DeleteFunc(s.running, func(r *Subprocess) bool { return r == sub })
	}
	s.finished = append(s.finished, batch...)
	s.mu.Unlock()

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
