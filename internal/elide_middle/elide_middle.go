package elide_middle

import (
	"strings"
)

// ansiColorSequenceIterator iterates over ANSI color sequences (\x1b[...m)
// in an input string. Note that this ignores non-color related ANSI sequences.
type ansiColorSequenceIterator struct {
	input    string
	curStart int
	curEnd   int
}

func newAnsiColorSequenceIterator(input string) *ansiColorSequenceIterator {
	iter := &ansiColorSequenceIterator{input: input}
	iter.findNextSequenceFrom(0)
	return iter
}

// hasSequence returns true if an ANSI sequence was found.
func (a *ansiColorSequenceIterator) hasSequence() bool {
	return a.curEnd != 0
}

// sequenceStart returns the start of the current sequence.
func (a *ansiColorSequenceIterator) sequenceStart() int {
	return a.curStart
}

// sequenceEnd returns the end of the current sequence (index of first char after sequence).
func (a *ansiColorSequenceIterator) sequenceEnd() int {
	return a.curEnd
}

// sequenceSize returns the size of the current sequence in characters.
func (a *ansiColorSequenceIterator) sequenceSize() int {
	return a.curEnd - a.curStart
}

// sequenceContains returns true if inputIndex belongs to the current sequence.
func (a *ansiColorSequenceIterator) sequenceContains(inputIndex int) bool {
	return inputIndex >= a.curStart && inputIndex < a.curEnd
}

// nextSequence finds the next sequence, if any, from the input.
// Returns false if there is no more sequence.
func (a *ansiColorSequenceIterator) nextSequence() bool {
	if a.findNextSequenceFrom(a.curEnd) {
		return true
	}
	a.curStart = 0
	a.curEnd = 0
	return false
}

// reset resets iterator to start of input.
func (a *ansiColorSequenceIterator) reset() {
	a.curStart = 0
	a.curEnd = 0
	a.findNextSequenceFrom(0)
}

// isParameterChar returns true if ch is a valid ANSI parameter character (digit or semicolon).
func isParameterChar(ch byte) bool {
	return (ch >= '0' && ch <= '9') || ch == ';'
}

// findNextSequenceFrom finds the next sequence from the input, starting at 'from'.
// On success, returns true after setting curStart and curEnd; on failure, returns false.
func (a *ansiColorSequenceIterator) findNextSequenceFrom(from int) bool {
	idx := strings.IndexByte(a.input[from:], '\x1b')
	if idx < 0 {
		return false
	}
	seq := from + idx

	// The smallest possible color sequence is '\x1b[0m' and has four characters.
	if seq+4 > len(a.input) {
		return false
	}

	if a.input[seq+1] != '[' {
		return a.findNextSequenceFrom(seq + 1)
	}

	// Skip parameters (digits + ; separator)
	end := seq + 2
	for end < len(a.input) && isParameterChar(a.input[end]) {
		end++
	}

	if end == len(a.input) {
		return false // Incomplete sequence (no command).
	}

	if a.input[end] != 'm' {
		// Not a color sequence. Restart the search after the first
		// character following the [, in case this was a 3-char ANSI
		// sequence (which is ignored here).
		return a.findNextSequenceFrom(seq + 3)
	}

	// Found it!
	a.curStart = seq
	a.curEnd = end + 1
	return true
}

// visibleInputCharsIterator iterates over all characters of an input string,
// returning the visible position in the terminal and whether that specific
// character is visible (or part of an ANSI color sequence).
type visibleInputCharsIterator struct {
	inputSize  int
	inputIndex int
	visiblePos int
	ansiIter   *ansiColorSequenceIterator
}

func newVisibleInputCharsIterator(input string) *visibleInputCharsIterator {
	return &visibleInputCharsIterator{
		inputSize: len(input),
		ansiIter:  newAnsiColorSequenceIterator(input),
	}
}

// hasChar returns true if there is a character in the sequence.
func (v *visibleInputCharsIterator) hasChar() bool {
	return v.inputIndex < v.inputSize
}

// inputIdx returns current input index.
func (v *visibleInputCharsIterator) inputIdx() int {
	return v.inputIndex
}

// visiblePosition returns current visible position.
func (v *visibleInputCharsIterator) visiblePosition() int {
	return v.visiblePos
}

// isVisible returns true if the current input character is visible
// (i.e. not part of an ANSI color sequence).
func (v *visibleInputCharsIterator) isVisible() bool {
	return !v.ansiIter.sequenceContains(v.inputIndex)
}

// nextChar finds next character from the input.
func (v *visibleInputCharsIterator) nextChar() {
	if v.isVisible() {
		v.visiblePos++
	}
	v.inputIndex++
	if v.inputIndex == v.ansiIter.sequenceEnd() {
		v.ansiIter.nextSequence()
	}
}

// ElideMiddle elides the given string with "..." in the middle if the visible
// length exceeds maxWidth. Handles ANSI color sequences properly (they don't
// count toward visible width and are preserved in the output).
func ElideMiddle(str string, maxWidth int) string {
	if len(str) <= maxWidth {
		return str
	}

	// Look for an ESC character. If there is none, use a fast path
	// that avoids any intermediate allocations.
	if strings.IndexByte(str, '\x1b') < 0 {
		const ellipsisWidth = 3 // Space for "...".

		// If max width is too small, do not keep anything from the input.
		if maxWidth <= ellipsisWidth {
			return "..."[:maxWidth]
		}

		// Keep only |maxWidth - ellipsisWidth| visible characters from the input
		// which will be split into two spans separated by "...".
		remainingSize := maxWidth - ellipsisWidth
		leftSpanSize := remainingSize / 2
		rightSpanSize := remainingSize - leftSpanSize

		// Replace the gap in the input between the spans with "..."
		gapStart := leftSpanSize
		gapEnd := len(str) - rightSpanSize
		return str[:gapStart] + "..." + str[gapEnd:]
	}

	// Compute visible width.
	visibleWidth := len(str)
	for ansi := newAnsiColorSequenceIterator(str); ansi.hasSequence(); ansi.nextSequence() {
		visibleWidth -= ansi.sequenceSize()
	}

	if visibleWidth <= maxWidth {
		return str
	}

	// Compute the widths of the ellipsis, left span and right span visible space.
	ellipsisWidth := 3
	if maxWidth < 3 {
		ellipsisWidth = maxWidth
	}
	visibleLeftSpanSize := (maxWidth - ellipsisWidth) / 2
	visibleRightSpanSize := (maxWidth - ellipsisWidth) - visibleLeftSpanSize

	// Compute the gap of visible characters that will be replaced by
	// the ellipsis in visible space.
	visibleGapStart := visibleLeftSpanSize
	visibleGapEnd := visibleWidth - visibleRightSpanSize

	var result strings.Builder
	result.Grow(len(str))

	// Parse the input chars info to:
	//
	// 1) Append any characters belonging to the left span (visible or not).
	//
	// 2) Add the ellipsis ("..." truncated to ellipsisWidth).
	//    Note that its color is inherited from the left span chars
	//    which will never end with an ANSI sequence.
	//
	// 3) Append any ANSI sequence that appears inside the gap. This
	//    ensures the characters after the ellipsis appear with
	//    the right color.
	//
	// 4) Append any remaining characters (visible or not) to the result.
	iter := newVisibleInputCharsIterator(str)

	// Step 1 - determine left span length in input chars.
	for iter.hasChar() {
		if iter.visiblePosition() == visibleGapStart {
			break
		}
		iter.nextChar()
	}
	result.WriteString(str[:iter.inputIdx()])

	// Step 2 - Append the possibly-truncated ellipsis.
	result.WriteString("..."[:ellipsisWidth])

	// Step 3 - Append elided ANSI sequences to the result.
	for iter.hasChar() {
		if iter.visiblePosition() == visibleGapEnd {
			break
		}
		if !iter.isVisible() {
			result.WriteByte(str[iter.inputIdx()])
		}
		iter.nextChar()
	}

	// Step 4 - Append anything else.
	result.WriteString(str[iter.inputIdx():])

	return result.String()
}
