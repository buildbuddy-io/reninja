package parser

import "fmt"

func debugToken(token interface{}, context string) {
	fmt.Printf("DEBUG [%s]: %v\n", context, token)
}
