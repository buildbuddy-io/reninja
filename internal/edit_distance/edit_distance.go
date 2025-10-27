package edit_distance

import (
	"fmt"
)

func EditDistance(s1, s2 string, allowReplacements bool, maxEditDistance int) int {
	m := len(s1)
	n := len(s2)

	row := make([]int, n+1)
	for i := 1; i <= n; i++ {
		row[i] = i
	}

	for y := 1; y <= m; y++ {
		row[0] = y
		bestThisRow := row[0]

		previous := y - 1
		for x := 1; x <= n; x++ {
			oldRow := row[x]
			prevEqual := s1[y-1] == s2[x-1]
			if allowReplacements {
				a := 1
				if prevEqual {
					a = 0
				}
				row[x] = min(previous+a, min(row[x-1], row[x])+1)
			} else {
				if prevEqual {
					row[x] = previous
				} else {
					row[x] = min(row[x-1], row[x]) + 1
				}
			}
			previous = oldRow
			bestThisRow = min(bestThisRow, row[x])
			fmt.Printf("previous %d, bestThisRow: %d\n", previous, bestThisRow)
		}

		if maxEditDistance > 0 && bestThisRow > maxEditDistance {
			return maxEditDistance + 1
		}
	}
	return row[n]
}
