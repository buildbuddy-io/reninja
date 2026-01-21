package elide_middle

import (
	"testing"
)

var testInputs = []string{
	"01234567890123456789",
	"012345\x1b[0;35m67890123456789",
	"abcd\x1b[1;31mefg\x1b[0mhlkmnopqrstuvwxyz",
}

func BenchmarkElideMiddle(b *testing.B) {
	for i := 0; i < b.N; i++ {
		for _, input := range testInputs {
			inputLen := len(input)
			for maxWidth := inputLen; maxWidth > 0; maxWidth-- {
				_ = ElideMiddle(input, maxWidth)
			}
		}
	}
}

func BenchmarkElideMiddleNoAnsi(b *testing.B) {
	input := testInputs[0] // "01234567890123456789"
	inputLen := len(input)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for maxWidth := inputLen; maxWidth > 0; maxWidth-- {
			_ = ElideMiddle(input, maxWidth)
		}
	}
}

func BenchmarkElideMiddleWithAnsi(b *testing.B) {
	input := testInputs[2] // "abcd\x1b[1;31mefg\x1b[0mhlkmnopqrstuvwxyz"
	inputLen := len(input)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for maxWidth := inputLen; maxWidth > 0; maxWidth-- {
			_ = ElideMiddle(input, maxWidth)
		}
	}
}
