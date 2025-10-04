package exit_status

type ExitStatusType int64

const (
	ExitSuccess     ExitStatusType = 0
	ExitFailure     ExitStatusType = 1
	ExitInterrupted ExitStatusType = 130
)
