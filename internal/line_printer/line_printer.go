package line_printer

import (
	"fmt"
	"io"
	"os"

	"github.com/jwalton/go-supportscolor"
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

func New() *LinePrinter {
	return &LinePrinter{
		out:           os.Stdout,
		smartTerminal: isatty.IsTerminal(os.Stdout.Fd()),
		supportsColor: supportscolor.Stdout().SupportsColor,
	}
}

func (p *LinePrinter) SmartTerminal() bool {
	return p.smartTerminal
}

func (p *LinePrinter) SupportsColor() bool {
	return p.supportsColor
}

func (p *LinePrinter) printOrBuffer(data string) {
	if p.consoleLocked {
		p.outputBuffer += data
	} else {
		p.out.Write([]byte(data))
	}
}

func ElideMiddleInPlace(in string, width int) string {
	// TODO(tylerw): go write elide_middle.cc
	return in
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
		if err == nil {
			toPrint = ElideMiddleInPlace(toPrint, width)
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
	p.haveBlankLine = toPrint == "" || toPrint[0] == '\n'
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
