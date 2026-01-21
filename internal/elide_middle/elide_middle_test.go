package elide_middle

import (
	"testing"
)

// ANSI escape sequences for testing.
const (
	magenta = "\x1b[0;35m"
	nothing = "\x1b[m"
	red     = "\x1b[1;31m"
	reset   = "\x1b[0m"
)

func TestNothingToElide(t *testing.T) {
	input := "Nothing to elide in this short string."
	tests := []struct {
		width    int
		expected string
	}{
		{80, input},
		{38, input},
		{0, ""},
		{1, "."},
		{2, ".."},
		{3, "..."},
	}

	for _, tc := range tests {
		result := ElideMiddle(input, tc.width)
		if result != tc.expected {
			t.Errorf("ElideMiddle(%q, %d) = %q, want %q", input, tc.width, result, tc.expected)
		}
	}
}

func TestElideInTheMiddle(t *testing.T) {
	input := "01234567890123456789"
	tests := []struct {
		width    int
		expected string
	}{
		{4, "...9"},
		{5, "0...9"},
		{9, "012...789"},
		{10, "012...6789"},
		{11, "0123...6789"},
		{19, "01234567...23456789"},
		{20, "01234567890123456789"},
	}

	for _, tc := range tests {
		result := ElideMiddle(input, tc.width)
		if result != tc.expected {
			t.Errorf("ElideMiddle(%q, %d) = %q, want %q", input, tc.width, result, tc.expected)
		}
	}
}

func TestElideAnsiEscapeCodes(t *testing.T) {
	// Test 1: MAGENTA in the middle
	input1 := "012345" + magenta + "67890123456789"
	tests1 := []struct {
		width    int
		expected string
	}{
		{10, "012..." + magenta + "6789"},
		{19, "012345" + magenta + "67...23456789"},
	}
	for _, tc := range tests1 {
		result := ElideMiddle(input1, tc.width)
		if result != tc.expected {
			t.Errorf("ElideMiddle(input1, %d) = %q, want %q", tc.width, result, tc.expected)
		}
	}

	// Test 2: NOTHING sequence
	input2 := "Nothing " + nothing + " string."
	result2 := ElideMiddle(input2, 18)
	expected2 := "Nothing " + nothing + " string."
	if result2 != expected2 {
		t.Errorf("ElideMiddle(input2, 18) = %q, want %q", result2, expected2)
	}

	input3 := "0" + nothing + "1234567890123456789"
	result3 := ElideMiddle(input3, 10)
	expected3 := "0" + nothing + "12...6789"
	if result3 != expected3 {
		t.Errorf("ElideMiddle(input3, 10) = %q, want %q", result3, expected3)
	}

	// Test 3: RED and RESET sequences with progressive widths
	input4 := "abcd" + red + "efg" + reset + "hlkmnopqrstuvwxyz"
	tests4 := []struct {
		width    int
		expected string
	}{
		{0, "" + red + reset},
		{1, "." + red + reset},
		{2, ".." + red + reset},
		{3, "..." + red + reset},
		{4, "..." + red + reset + "z"},
		{5, "a..." + red + reset + "z"},
		{6, "a..." + red + reset + "yz"},
		{7, "ab..." + red + reset + "yz"},
		{8, "ab..." + red + reset + "xyz"},
		{9, "abc..." + red + reset + "xyz"},
		{10, "abc..." + red + reset + "wxyz"},
		{11, "abcd..." + red + reset + "wxyz"},
		{12, "abcd..." + red + reset + "vwxyz"},
		{15, "abcd" + red + "ef..." + reset + "uvwxyz"},
		{16, "abcd" + red + "ef..." + reset + "tuvwxyz"},
		{17, "abcd" + red + "efg..." + reset + "tuvwxyz"},
		{18, "abcd" + red + "efg..." + reset + "stuvwxyz"},
		{19, "abcd" + red + "efg" + reset + "h...stuvwxyz"},
	}
	for _, tc := range tests4 {
		result := ElideMiddle(input4, tc.width)
		if result != tc.expected {
			t.Errorf("ElideMiddle(input4, %d) = %q, want %q", tc.width, result, tc.expected)
		}
	}

	// Test 4: Another sequence with RED A RESET BC
	input5 := "abcdef" + red + "A" + reset + "BC"
	tests5 := []struct {
		width    int
		expected string
	}{
		{4, "..." + red + reset + "C"},
		{5, "a..." + red + reset + "C"},
		{6, "a..." + red + reset + "BC"},
		{7, "ab..." + red + reset + "BC"},
		{8, "ab..." + red + "A" + reset + "BC"},
		{9, "abcdef" + red + "A" + reset + "BC"},
	}
	for _, tc := range tests5 {
		result := ElideMiddle(input5, tc.width)
		if result != tc.expected {
			t.Errorf("ElideMiddle(input5, %d) = %q, want %q", tc.width, result, tc.expected)
		}
	}
}
