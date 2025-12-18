package build_log

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/metrics"
	"github.com/buildbuddy-io/gin/internal/timestamp"
	rapidhash "github.com/buildbuddy-io/gin/third_party/rapidhash_v1"
)

const (
	fileSignature          = "# ninja log v%d\n"
	currentVersion         = 7
	oldestSupportedVersion = 7

	// BuildLogVersion is the current build log format version
	BuildLogVersion = "# ninja log v6\n"

	// LogEntry signature for binary format
	LogSignature = 0x6e696e6a // "ninj"

	rapidHashSeed uint64 = 0xBDD89AA982704029
)

type Entries = map[string]*LogEntry

type BuildLogUser interface {
	IsPathDead(s string) bool
}

func HashCommand(command string) uint64 {
	return rapidhash.RapidhashWithSeed([]byte(command), rapidHashSeed)
}

// LogEntry represents a single build log entry
type LogEntry struct {
	Output      string
	StartTime   int64               // Milliseconds since epoch
	EndTime     int64               // Milliseconds since epoch
	Mtime       timestamp.TimeStamp // TODO(tylerw): move to own package.
	CommandHash uint64
}

// BuildLog tracks build history and command execution
type BuildLog struct {
	entries           map[string]*LogEntry // Keyed by output path
	logFile           *os.File
	logFilePath       string
	needsRecompaction bool
}

func NewBuildLog() *BuildLog {
	return &BuildLog{
		entries:           make(map[string]*LogEntry),
		needsRecompaction: false,
	}
}

func WriteEntry(f *os.File, entry *LogEntry) error {
	_, err := fmt.Fprintf(f, "%d\t%d\t%d\t%s\t%x\n",
		entry.StartTime, entry.EndTime, entry.Mtime,
		entry.Output, entry.CommandHash)
	return err
}

func (b *BuildLog) OpenForWrite(path string, user BuildLogUser) error {
	if b.needsRecompaction {
		if err := b.Recompact(path, user); err != nil {
			return err
		}
	}
	if b.logFile != nil {
		panic("logFile was already opened!")
	}

	// we don't actually open the file right now, but will
	// do so on the first write attempt
	b.logFilePath = path
	return nil
}

// RecordCommand records a command execution
func (b *BuildLog) RecordCommand(edge *graph.Edge, start, end int64, mtime timestamp.TimeStamp) error {
	command := edge.EvaluateCommand(true)
	commandHash := HashCommand(command)

	for _, out := range edge.Outputs() {
		path := out.Path()
		entry, ok := b.entries[path]
		if !ok {
			entry = &LogEntry{
				Output: out.Path(),
			}
			b.entries[path] = entry
		}
		entry.CommandHash = commandHash
		entry.StartTime = start
		entry.EndTime = end
		entry.Mtime = mtime

		if err := b.OpenForWriteIfNeeded(); err != nil {
			return err
		}
		if b.logFile != nil {
			if err := WriteEntry(b.logFile, entry); err != nil {
				return err
			}
			// no fflush, file is written immediately.
		}
	}
	return nil
}

func (b *BuildLog) Close() error {
	if err := b.OpenForWriteIfNeeded(); err != nil {
		return err
	}
	if b.logFile != nil {
		if err := b.logFile.Close(); err != nil {
			return err
		}
	}
	b.logFile = nil
	return nil
}

func (b *BuildLog) OpenForWriteIfNeeded() error {
	if b.logFile != nil || len(b.logFilePath) == 0 {
		return nil
	}
	f, err := os.OpenFile(b.logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	b.logFile = f

	// TODO(tylerw): there is no buffering here, add some.
	// cpp version uses line buffering which prolly requires some bufio thing.

	pos, err := b.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if pos == 0 {
		if _, err := fmt.Fprintf(b.logFile, fileSignature, currentVersion); err != nil {
			return err
		}
	}
	return nil
}

// cpp version returns LoadStatus -- we're just returning an error,
// caller can look at it to determine if it was notfound or something else.
func (b *BuildLog) Load(path string) error {
	defer metrics.Record(".ninja_log load")()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	logVersion := 0
	uniqueEntryCount := 0
	totalEntryCount := 0

	reader := bufio.NewReader(f)
	successfullyParsedVersion := false

	var buf []byte
	for {
		data, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		buf = append(buf, data...)
		if isPrefix {
			moreData, isPrefix, err := reader.ReadLine()
			if err != nil {
				return err
			}
			buf = append(buf, moreData...)
			if isPrefix {
				continue
			}
		}
		if len(buf) > 256000 {
			buf = buf[:0]
			continue // Skip lines > 256kB to match ninja
		}
		lineStr := string(buf)
		buf = buf[:0]

		if logVersion == 0 {
			if _, err := fmt.Sscanf(lineStr, fileSignature, &logVersion); err != nil {
				return err
			}
			if logVersion < oldestSupportedVersion {
				defer os.Remove(path)
				return fmt.Errorf("build log version is too old; starting over")
			}
			if logVersion > currentVersion {
				defer os.Remove(path)
				return fmt.Errorf("build log version is too new; starting over")
			}
			successfullyParsedVersion = true
			continue
		}
		const fieldSeparator = "\t"
		parts := strings.Split(lineStr, fieldSeparator)
		if len(parts) != 5 {
			continue
		}

		startTimeStr := parts[0]
		endTimeStr := parts[1]
		mTimeStr := parts[2]
		outPath := parts[3]
		commandHashStr := parts[4]

		startTime, err := strconv.ParseInt(startTimeStr, 10, 64)
		if err != nil {
			return err
		}

		endTime, err := strconv.ParseInt(endTimeStr, 10, 64)
		if err != nil {
			return err
		}

		mTimeInt, err := strconv.ParseInt(mTimeStr, 10, 64)
		if err != nil {
			return err
		}
		mTime := timestamp.TimeStamp(mTimeInt)

		var commandHash uint64
		if _, err := fmt.Sscanf(commandHashStr, "%x", &commandHash); err != nil {
			return err
		}

		entry, ok := b.entries[outPath]
		if !ok {
			entry = &LogEntry{
				Output: outPath,
			}
			b.entries[outPath] = entry
			uniqueEntryCount++
		}
		totalEntryCount++

		entry.CommandHash = commandHash
		entry.StartTime = startTime
		entry.EndTime = endTime
		entry.Mtime = mTime
	}
	if !successfullyParsedVersion {
		return nil // file was empty
	}

	// Decide whether it's time to rebuild the log:
	// - if we're upgrading versions
	// - if it's getting large
	minCompactionEntryCount := 100
	compactionRatio := 3

	if logVersion < currentVersion {
		b.needsRecompaction = true
	} else if totalEntryCount > minCompactionEntryCount &&
		totalEntryCount > uniqueEntryCount*compactionRatio {
		b.needsRecompaction = true
	}
	return nil
}

func (b *BuildLog) LookupByOutput(path string) *LogEntry {
	return b.entries[path]
}

func (b *BuildLog) Entries() map[string]*LogEntry {
	return b.entries
}

func (b *BuildLog) Recompact(path string, user BuildLogUser) error {
	defer metrics.Record(".ninja_log recompact")()
	if err := b.Close(); err != nil {
		return err
	}
	tempPath := path + ".recompact"
	f, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(f, fileSignature, currentVersion); err != nil {
		f.Close()
		return err
	}
	deadOutputs := make([]string, 0)
	for path, entry := range b.entries {
		if user.IsPathDead(path) {
			deadOutputs = append(deadOutputs, path)
			continue
		}
		if err := WriteEntry(f, entry); err != nil {
			f.Close()
			return err
		}
	}
	for _, output := range deadOutputs {
		delete(b.entries, output)
	}

	if err := f.Close(); err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return err
	}

	return nil
}

func (b *BuildLog) Restat(path string, diskInterface disk.Interface, outputCount int, outputs []string) error {
	defer metrics.Record(".ninja_log restat")()
	if err := b.Close(); err != nil {
		return err
	}
	tempPath := path + ".restat"
	f, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(f, fileSignature, currentVersion); err != nil {
		f.Close()
		return err
	}
	for _, entry := range b.entries {
		skip := outputCount > 0
		for j := 0; j < outputCount; j++ {
			if entry.Output == outputs[j] {
				skip = false
				break
			}
		}

		if !skip {
			mtime, err := diskInterface.Stat(entry.Output)
			if err != nil {
				f.Close()
				return err
			}
			entry.Mtime = mtime
		}

		if err := WriteEntry(f, entry); err != nil {
			f.Close()
			return err
		}
	}

	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return err
	}

	return nil
}
