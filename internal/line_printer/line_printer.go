package line_printer

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/buildbuddy-io/reninja/internal/elide_middle"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

type LineType int

const (
	Full LineType = iota
	Elide
)

// TODO(tylerw): this is probably pretty alloc heavy because of string use.
// Profile and fix.
type LinePrinter struct {
	out io.Writer

	smartTerminal bool
	supportsColor bool
	haveBlankLine bool
	consoleLocked bool
	lineBuffer    string
	lineType      LineType
	outputBuffer  string
}

func SmartTerminal() bool {
	// A smart terminal supports escape sequences. Check that stdout is a tty
	// and that TERM is set (even if empty) and not "dumb".
	// This matches C++ ninja behavior: TERM="" is smart, TERM not set is not.
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return false
	}
	term, isSet := os.LookupEnv("TERM")
	if !isSet {
		return false
	}
	return term != "dumb"
}

func SupportsColor() bool {
	// C++ ninja: supports_color = smart_terminal, then check CLICOLOR_FORCE
	if SmartTerminal() {
		return true
	}
	// CLICOLOR_FORCE overrides when not a smart terminal
	if clicolor := os.Getenv("CLICOLOR_FORCE"); clicolor != "" && clicolor != "0" {
		return true
	}
	return false
}

func New() *LinePrinter {
	return NewCustom(os.Stdout, SmartTerminal(), SupportsColor())
}

func NewCustom(out io.Writer, smartTerminal, supportsColor bool) *LinePrinter {
	return &LinePrinter{
		out:           out,
		haveBlankLine: true,
		consoleLocked: false,
		smartTerminal: smartTerminal,
		supportsColor: supportsColor,
	}
}

func (p *LinePrinter) SmartTerminal() bool {
	return p.smartTerminal
}

func (p *LinePrinter) SupportsColor() bool {
	return p.supportsColor
}

// Esc returns an ANSI escape sequence for the given codes.
// If either stdout or stderr is not a tty, it returns an empty string.
// It is intended only for text styling, where dropping the escape sequence
// still produces usable text content.
//
// Example:
//
//	terminal.Esc(1, 31) + "Bold red text" + terminal.Esc() + " normal text"
func (p *LinePrinter) Esc(codes ...int) string {
	if !p.supportsColor {
		return ""
	}
	strs := make([]string, 0, len(codes))
	for _, code := range codes {
		// Missing numbers are treated as 0, and some sequences may treat
		// missing values as meaningful, so coerce 0 to empty string so that
		// missing values can be represented.
		if code == 0 {
			strs = append(strs, "")
		} else {
			strs = append(strs, strconv.Itoa(code))
		}
	}
	return "\x1b[" + strings.Join(strs, ";") + "m"
}

func (p *LinePrinter) printOrBuffer(data string) {
	if p.consoleLocked {
		p.outputBuffer += data
	} else {
		p.out.Write([]byte(data))
	}
}

func (p *LinePrinter) Print(toPrint string, lineType LineType) {
	if p.consoleLocked {
		p.lineBuffer = toPrint
		p.lineType = lineType
		return
	}

	if p.smartTerminal {
		p.out.Write([]byte("\r"))
	}

	if p.smartTerminal && lineType == Elide {
		width, _, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil && width > 0 {
			toPrint = elide_middle.ElideMiddle(toPrint, width)
		}
		fmt.Fprintf(p.out, "%s", toPrint)
		fmt.Fprintf(p.out, "\x1B[K")
		p.haveBlankLine = false
	} else {
		fmt.Fprintf(p.out, "%s\n", toPrint)
	}
}

func (p *LinePrinter) PrintOnNewline(toPrint string) {
	if p.consoleLocked && p.lineBuffer != "" {
		p.outputBuffer += p.lineBuffer + "\n"
		p.lineBuffer = ""
	}
	if !p.haveBlankLine {
		p.printOrBuffer("\n")
	}
	if toPrint != "" {
		p.printOrBuffer(toPrint)
	}
	p.haveBlankLine = toPrint == "" || toPrint[len(toPrint)-1] == '\n'
}

func (p *LinePrinter) SetConsoleLocked(locked bool) {
	if locked == p.consoleLocked {
		return
	}
	if locked {
		p.PrintOnNewline("")
	}
	p.consoleLocked = locked

	if !locked {
		p.PrintOnNewline(p.outputBuffer)
		if len(p.lineBuffer) > 0 {
			p.Print(p.lineBuffer, p.lineType)
		}
		p.outputBuffer = ""
		p.lineBuffer = ""
	}
}

// PrintNextLine moves to the next line and prints line there, elided to
// terminal width. Must only be called on smart terminals!
func (p *LinePrinter) PrintNextLine(line string) {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && width > 0 {
		line = elide_middle.ElideMiddle(line, width)
	}
	fmt.Fprintf(p.out, "\n%s\x1B[K", line)
}

// ClearNextLine moves down one line, erases it completely, and returns the
// cursor to column 0. Must only be called on smart terminals!
func (p *LinePrinter) ClearNextLine() {
	p.out.Write([]byte("\x1B[1B\x1B[2K\r"))
}

// MoveUp moves the cursor up n lines. Must only be called on smart
// terminals!
func (p *LinePrinter) MoveUp(n int) {
	fmt.Fprintf(p.out, "\x1B[%dA", n)
}
