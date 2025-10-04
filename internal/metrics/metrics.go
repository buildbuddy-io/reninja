package metrics

import (
	"time"
)

// TODO(tylerw); add da macros

func GetTimeMillis() int64 {
	return time.Now().UnixMilli()
}
