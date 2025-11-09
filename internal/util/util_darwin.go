//go:build (darwin && !ios) && (amd64 || arm64)
package util

/*
#include <stdlib.h>
*/
import "C"

func GetLoadAverage() float64 {
	avg := []C.double{0, 0, 0}

	C.getloadavg(&avg[0], C.int(len(avg)))

	return float64(avg[0])
}
