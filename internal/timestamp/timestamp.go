package timestamp

// When considering file modification times we only care to compare
// them against one another -- we never convert them to an absolute
// real time.  On POSIX we use timespec (seconds&nanoseconds since epoch)
// and on Windows we use a different value.  Both fit in an int64.
type TimeStamp int64

const (
	// TimeStampUnknown indicates file hasn't been examined
	TimeStampUnknown TimeStamp = -1
	// TimeStampMissing indicates file doesn't exist
	TimeStampMissing TimeStamp = 0
)
