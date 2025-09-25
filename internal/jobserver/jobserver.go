// / Jobserver provides types related to managing a pool of "job slots"
// / using the GNU Make jobserver ptocol described at:
// /
// / https://www.gnu.org/software/make/manual/html_node/Job-Slots.html
// /
package jobserver

import (
	"fmt"
	"runtime"
	"strings"
)

const (
	implicitValue = 256
)

// / A Jobserver::Slot models a single job slot that can be acquired from.
// / or released to a jobserver pool. This class is move-only, and can
// / wrap three types of values:
// /
// / - An "invalid" value (the default), used to indicate errors, e.g.
// /   that no slot could be acquired from the pool.
// /
// / - The "implicit" value, used to model the job slot that is implicitly
// /   assigned to a jobserver client by the parent process that spawned
// /   it.
// /
// / - The "explicit" values, which correspond to an actual byte read from
// /   the slot pool's pipe (for Posix), or a semaphore decrement operation
// /   (for Windows).
// /
// / Use IsValid(), IsImplicit(), HasValue() to test for categories.
// /
// / TECHNICAL NOTE: This design complies with the requirements laid out
// / on https://www.gnu.org/software/make/manual/html_node/POSIX-Jobserver.html
// / which requires clients to write back the exact token values they
// / received from a Posix pipe.
// /
// / Note that *currently* all pool implementations write the same token
// / values to the pipe ('+' for GNU Make, and '|' for the Rust jobserver),
// / and do not care about the values written back by clients.
// /
type Slot int16

func NewSlot() Slot {
	return Slot(-1)
}

func CreateExplicitSlot(value uint8) Slot {
	return Slot(int16(value))
}

func CreateImplicitSlot() Slot {
	return Slot(implicitValue)
}

func (s Slot) Valid() bool {
	return s >= 0
}

func (s Slot) Implicit() bool {
	return s == implicitValue
}

func (s Slot) Explicit() bool {
	return s.Valid() && !s.Implicit()
}

func (s Slot) ExplicitValue() uint8 {
	if !s.Explicit() {
		panic("not explicit")
	}
	return uint8(s)
}

// / Different implementation modes for the slot pool.
// /
// / kModeNone means there is no pool.
// /
// / kModePipe means that `--jobserver-auth=R,W` is used to
// /    pass a pair of file descriptors to client processes. This also
// /    matches `--jobserver-fds=R,W` which is an old undocumented
// /    variant of the same scheme. This mode is not supported by
// /    Ninja, but recognized by the parser.
// /
// / kModePosixFifo means that `--jobserver-auth=fifo:PATH` is used to
// /    pass the path of a Posix FIFO to client processes. This is not
// /    supported on Windows. Implemented by GNU Make 4.4 and above
// /    when `--jobserver-style=fifo` is used.
// /
// / kModeWin32Semaphore means that `--jobserver-auth=SEMAPHORE_NAME` is
// /    used to pass the name of a Win32 semaphore to client processes.
// /    This is not supported on Posix.
// /
// / kModeDefault is the default mode to enable on the current platform.
// /    This is an alias for kModeWin32Semaphore on Windows ,and
// /    kModePosixFifo on Posix.
type ConfigMode int

const (
	ModeNone ConfigMode = iota
	ModePipe
	ModePosixFifo
	ModeWin32Semaphore
	ModeDefault
)

// / A Jobserver::Config models how to access or implement a GNU jobserver
// / implementation.
type Config struct {
	Mode ConfigMode
	Path string
}

func NewConfig(mode ConfigMode) *Config {
	return &Config{
		Mode: mode,
	}
}

// / Return true if this instance matches an active implementation mode.
// / This does not try to validate configuration parameters though.
func (c Config) HasMode() bool {
	return c.Mode != ModeNone
}

type Jobserver struct{}

func GetFileDescriptorPair(input string) (*Config, bool) {
	readFD := 1
	writeFD := 1
	if _, err := fmt.Sscanf(input, "%d,%d", &readFD, &writeFD); err != nil {
		return nil, false
	}

	// From
	// https://www.gnu.org/software/make/manual/html_node/POSIX-Jobserver.html Any
	// negative descriptor means the feature is disabled.
	var mode ConfigMode
	if readFD < 0 || writeFD < 0 {
		mode = ModeNone
	} else {
		mode = ModePipe
	}

	return NewConfig(mode), true
}

// / Parse the value of a MAKEFLAGS environment variable. On success return
// / true and set |*config|. On failure, return false and set |*error| to
// / explain what's wrong. If |makeflags_env| is nullptr or an empty string,
// / this returns success and sets |config->mode| to Config::kModeNone.
func (j *Jobserver) ParseMakeFlagsValue(makeflagsEnv string) (*Config, error) {
	conf := NewConfig(ModeNone) // default config
	if makeflagsEnv == "" {
		/// Return default Config instance with kModeNone if input is null or empty.
		return conf, nil
	}

	// Decompose input into vector of space or tab separated string pieces.
	args := strings.Fields(makeflagsEnv)
	if len(args) > 0 && args[0][0] != '-' && strings.Contains(args[0], "n") {
		return conf, nil
	}

	// Loop over all arguments, the last one wins, except in case of errors.
	for _, arg := range args {
		// Handle --jobserver-auth=... here.
		if prefix, value, ok := strings.Cut(arg, "--jobserver-auth="); ok && prefix == "" {
			if newConf, ok := GetFileDescriptorPair(value); ok {
				conf = newConf
				continue
			}
			if _, fifoPath, ok := strings.Cut(value, "fifo:"); ok {
				conf.Mode = ModePosixFifo
				conf.Path = fifoPath
			} else {
				conf.Mode = ModeWin32Semaphore
				conf.Path = value
			}
			continue
		}

		// Handle --jobserver-fds which is an old undocumented variant of
		// --jobserver-auth that only accepts a pair of file descriptor.
		// This was replaced by --jobserver-auth=R,W in GNU Make 4.2.
		if prefix, value, ok := strings.Cut(arg, "--jobserver-fds="); ok && prefix == "" {
			if newConf, ok := GetFileDescriptorPair(value); !ok {
				return nil, fmt.Errorf("Invalid file descriptor pair [%q]", value)
			} else {
				conf = newConf
				conf.Mode = ModePipe
			}
			continue
		}

		// Ignore this argument. This assumes that MAKEFLAGS does not
		// use spaces to separate the option from its argument, e.g.
		// `--jobserver-auth <something>`, which has been confirmed with
		// Make 4.3, even if it receives such a value in its own env.
	}
	return conf, nil
}

// / A variant of ParseMakeFlagsValue() that will return an error if the parsed
// / result is not compatible with the native system. I.e.:
// /
// /   --jobserver-auth=R,W is not supported on any system (but recognized to
// /       provide a relevant error message to the user).
// /
// /   --jobserver-auth=NAME onlw works on Windows.
// /
// /   --jobserver-auth=fifo:PATH only works on Posix.
// /
func (j *Jobserver) ParseNativeMakeFlagsValue(makeflagsEnv string) (*Config, error) {
	conf, err := j.ParseMakeFlagsValue(makeflagsEnv)
	if err != nil {
		return nil, err
	}

	if conf.Mode == ModePipe {
		return nil, fmt.Errorf("Pipe-based protocol is not supported!")
	}
	if runtime.GOOS == "windows" {
		if conf.Mode == ModePosixFifo {
			return nil, fmt.Errorf("FIFO mode is not supported on Windows!")
		}
	} else {
		if conf.Mode == ModeWin32Semaphore {
			return nil, fmt.Errorf("Semaphore mode is not supported on Posix!")
		}
	}
	return conf, nil
}

type Client interface {
	Create(config *Config) error
	TryAcquire() Slot
	Release(slot Slot)
}
