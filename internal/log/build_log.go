// Copyright 2024 The Ninja-Go Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package log

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buildbuddy-io/gin/internal/graph"
)

const (
	// BuildLogVersion is the current build log format version
	BuildLogVersion = "# ninja log v6\n"
	
	// LogEntry signature for binary format
	LogSignature = 0x6e696e6a // "ninj"
)

// LogEntry represents a single build log entry
type LogEntry struct {
	Output         string
	Command        string
	StartTime      int64 // Milliseconds since epoch
	EndTime        int64 // Milliseconds since epoch
	RestatMtime    graph.TimeStamp
	CommandHash    uint64
}

// BuildLog tracks build history and command execution
type BuildLog struct {
	mu       sync.RWMutex
	entries  map[string]*LogEntry // Keyed by output path
	logFile  string
	dirty    bool
	needsRecompaction bool
}

// NewBuildLog creates a new BuildLog
func NewBuildLog() *BuildLog {
	return &BuildLog{
		entries: make(map[string]*LogEntry),
	}
}

// OpenForWrite opens the build log for writing
func (b *BuildLog) OpenForWrite(path string, buildDir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	b.logFile = path
	
	// Load existing log if it exists
	if err := b.load(buildDir); err != nil {
		// If loading fails, start fresh
		b.entries = make(map[string]*LogEntry)
	}
	
	return nil
}

// RecordCommand records a command execution
func (b *BuildLog) RecordCommand(edge *graph.Edge, startMs, endMs int64, restatMtime graph.TimeStamp) {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	command := edge.EvaluateCommand(false)
	commandHash := hashCommand(command)
	
	for _, output := range edge.Outputs() {
		entry := &LogEntry{
			Output:      output.Path(),
			Command:     command,
			StartTime:   startMs,
			EndTime:     endMs,
			RestatMtime: restatMtime,
			CommandHash: commandHash,
		}
		
		b.entries[output.Path()] = entry
		b.dirty = true
	}
}

// GetEntry returns a log entry for an output path
func (b *BuildLog) GetEntry(path string) *LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	return b.entries[path]
}

// CommandLogPath returns the path for a command's log
func (b *BuildLog) CommandLogPath(output string) string {
	// Use a hash of the output path for the log file name
	hash := hashCommand(output)
	return fmt.Sprintf(".ninja_log_%x", hash)
}

// Save writes the build log to disk
func (b *BuildLog) Save() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	if !b.dirty || b.logFile == "" {
		return nil
	}
	
	// Write to temp file first for atomic update
	tempFile := b.logFile + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return err
	}
	
	writer := bufio.NewWriter(file)
	
	// Write header
	if _, err := writer.WriteString(BuildLogVersion); err != nil {
		file.Close()
		os.Remove(tempFile)
		return err
	}
	
	// Sort entries for consistent output
	var paths []string
	for path := range b.entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	
	// Write entries
	for _, path := range paths {
		entry := b.entries[path]
		line := fmt.Sprintf("%d\t%d\t%d\t%s\t%x\n",
			entry.StartTime,
			entry.EndTime,
			entry.RestatMtime,
			entry.Output,
			entry.CommandHash)
		
		if _, err := writer.WriteString(line); err != nil {
			file.Close()
			os.Remove(tempFile)
			return err
		}
	}
	
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(tempFile)
		return err
	}
	
	if err := file.Close(); err != nil {
		os.Remove(tempFile)
		return err
	}
	
	// Atomic rename
	if err := os.Rename(tempFile, b.logFile); err != nil {
		os.Remove(tempFile)
		return err
	}
	
	b.dirty = false
	return nil
}

// load reads the build log from disk
func (b *BuildLog) load(buildDir string) error {
	if b.logFile == "" {
		return nil
	}
	
	file, err := os.Open(b.logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing log
		}
		return err
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	
	// Read header
	if !scanner.Scan() {
		return fmt.Errorf("empty build log")
	}
	
	header := scanner.Text() + "\n"
	if header != BuildLogVersion {
		return fmt.Errorf("unsupported build log version: %s", header)
	}
	
	// Read entries
	lineNum := 1
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		parts := strings.Split(line, "\t")
		if len(parts) < 5 {
			// Invalid line, skip
			continue
		}
		
		startTime, _ := strconv.ParseInt(parts[0], 10, 64)
		endTime, _ := strconv.ParseInt(parts[1], 10, 64)
		restatMtime, _ := strconv.ParseInt(parts[2], 10, 64)
		output := parts[3]
		var commandHash uint64
		if len(parts) > 4 {
			fmt.Sscanf(parts[4], "%x", &commandHash)
		}
		
		// Adjust path relative to build dir if needed
		if buildDir != "" && !filepath.IsAbs(output) {
			output = filepath.Join(buildDir, output)
		}
		
		entry := &LogEntry{
			Output:      output,
			StartTime:   startTime,
			EndTime:     endTime,
			RestatMtime: graph.TimeStamp(restatMtime),
			CommandHash: commandHash,
		}
		
		b.entries[output] = entry
	}
	
	return scanner.Err()
}

// Recompact rewrites the log file to remove duplicate entries
func (b *BuildLog) Recompact(path string, buildDir string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	// Load the log
	b.logFile = path
	if err := b.load(buildDir); err != nil {
		return err
	}
	
	// Mark as dirty to force save
	b.dirty = true
	
	// Save will write a compacted version
	return b.Save()
}

// GetEntries returns all log entries
func (b *BuildLog) GetEntries() map[string]*LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	// Return a copy
	entries := make(map[string]*LogEntry, len(b.entries))
	for k, v := range b.entries {
		entryCopy := *v
		entries[k] = &entryCopy
	}
	return entries
}

// RemoveEntry removes an entry from the log
func (b *BuildLog) RemoveEntry(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	if _, exists := b.entries[path]; exists {
		delete(b.entries, path)
		b.dirty = true
	}
}

// Clear removes all entries
func (b *BuildLog) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	b.entries = make(map[string]*LogEntry)
	b.dirty = true
}

// IsDirty returns whether the log has unsaved changes
func (b *BuildLog) IsDirty() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dirty
}

// SetLogPath sets the log file path
func (b *BuildLog) SetLogPath(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logFile = path
}

// GetLogPath returns the log file path
func (b *BuildLog) GetLogPath() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.logFile
}

// hashCommand computes a hash of a command string
func hashCommand(command string) uint64 {
	// Simple FNV-1a hash
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	
	hash := uint64(offset64)
	for i := 0; i < len(command); i++ {
		hash ^= uint64(command[i])
		hash *= prime64
	}
	return hash
}

// Stats represents build log statistics
type Stats struct {
	TotalEntries   int
	TotalBuildTime time.Duration
	AverageBuildTime time.Duration
	OldestEntry    time.Time
	NewestEntry    time.Time
}

// GetStats returns statistics about the build log
func (b *BuildLog) GetStats() *Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	if len(b.entries) == 0 {
		return &Stats{}
	}
	
	stats := &Stats{
		TotalEntries: len(b.entries),
	}
	
	var totalMs int64
	var oldest, newest int64
	
	for _, entry := range b.entries {
		duration := entry.EndTime - entry.StartTime
		totalMs += duration
		
		if oldest == 0 || entry.StartTime < oldest {
			oldest = entry.StartTime
		}
		if entry.EndTime > newest {
			newest = entry.EndTime
		}
	}
	
	stats.TotalBuildTime = time.Duration(totalMs) * time.Millisecond
	if stats.TotalEntries > 0 {
		stats.AverageBuildTime = stats.TotalBuildTime / time.Duration(stats.TotalEntries)
	}
	
	if oldest > 0 {
		stats.OldestEntry = time.Unix(0, oldest*int64(time.Millisecond))
	}
	if newest > 0 {
		stats.NewestEntry = time.Unix(0, newest*int64(time.Millisecond))
	}
	
	return stats
}

// FindEntriesForCommand finds all entries that match a command pattern
func (b *BuildLog) FindEntriesForCommand(pattern string) []*LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	var matches []*LogEntry
	for _, entry := range b.entries {
		if strings.Contains(entry.Command, pattern) {
			entryCopy := *entry
			matches = append(matches, &entryCopy)
		}
	}
	
	return matches
}

// PruneOldEntries removes entries older than the specified duration
func (b *BuildLog) PruneOldEntries(maxAge time.Duration) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	removed := 0
	
	for path, entry := range b.entries {
		if entry.EndTime < cutoff {
			delete(b.entries, path)
			removed++
			b.dirty = true
		}
	}
	
	return removed
}

// Writer provides a way to write build log entries incrementally
type Writer struct {
	file   *os.File
	writer *bufio.Writer
}

// NewWriter creates a new log writer
func NewWriter(path string) (*Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	
	// Check if file is empty and write header
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	
	writer := bufio.NewWriter(file)
	
	if stat.Size() == 0 {
		if _, err := writer.WriteString(BuildLogVersion); err != nil {
			file.Close()
			return nil, err
		}
	}
	
	return &Writer{
		file:   file,
		writer: writer,
	}, nil
}

// WriteEntry writes a single log entry
func (w *Writer) WriteEntry(entry *LogEntry) error {
	line := fmt.Sprintf("%d\t%d\t%d\t%s\t%x\n",
		entry.StartTime,
		entry.EndTime,
		entry.RestatMtime,
		entry.Output,
		entry.CommandHash)
	
	_, err := w.writer.WriteString(line)
	return err
}

// Flush flushes buffered data
func (w *Writer) Flush() error {
	return w.writer.Flush()
}

// Close closes the writer
func (w *Writer) Close() error {
	if err := w.writer.Flush(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// Reader provides a way to read build log entries
type Reader struct {
	scanner *bufio.Scanner
	file    *os.File
}

// NewReader creates a new log reader
func NewReader(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	
	scanner := bufio.NewScanner(file)
	
	// Read and verify header
	if !scanner.Scan() {
		file.Close()
		return nil, fmt.Errorf("empty build log")
	}
	
	header := scanner.Text() + "\n"
	if header != BuildLogVersion {
		file.Close()
		return nil, fmt.Errorf("unsupported build log version: %s", header)
	}
	
	return &Reader{
		scanner: scanner,
		file:    file,
	}, nil
}

// ReadEntry reads the next log entry
func (r *Reader) ReadEntry() (*LogEntry, error) {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		parts := strings.Split(line, "\t")
		if len(parts) < 5 {
			continue // Skip invalid lines
		}
		
		startTime, _ := strconv.ParseInt(parts[0], 10, 64)
		endTime, _ := strconv.ParseInt(parts[1], 10, 64)
		restatMtime, _ := strconv.ParseInt(parts[2], 10, 64)
		output := parts[3]
		var commandHash uint64
		if len(parts) > 4 {
			fmt.Sscanf(parts[4], "%x", &commandHash)
		}
		
		return &LogEntry{
			Output:      output,
			StartTime:   startTime,
			EndTime:     endTime,
			RestatMtime: graph.TimeStamp(restatMtime),
			CommandHash: commandHash,
		}, nil
	}
	
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	
	return nil, io.EOF
}

// Close closes the reader
func (r *Reader) Close() error {
	return r.file.Close()
}

// BinaryWriter provides binary format writing for better performance
type BinaryWriter struct {
	file   *os.File
	buffer *bytes.Buffer
}

// NewBinaryWriter creates a new binary format writer
func NewBinaryWriter(path string) (*BinaryWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	
	// Write signature
	if err := binary.Write(file, binary.LittleEndian, uint32(LogSignature)); err != nil {
		file.Close()
		return nil, err
	}
	
	// Write version
	if err := binary.Write(file, binary.LittleEndian, uint32(6)); err != nil {
		file.Close()
		return nil, err
	}
	
	return &BinaryWriter{
		file:   file,
		buffer: new(bytes.Buffer),
	}, nil
}

// WriteBinaryEntry writes an entry in binary format
func (w *BinaryWriter) WriteBinaryEntry(entry *LogEntry) error {
	w.buffer.Reset()
	
	// Write entry data to buffer
	binary.Write(w.buffer, binary.LittleEndian, entry.StartTime)
	binary.Write(w.buffer, binary.LittleEndian, entry.EndTime)
	binary.Write(w.buffer, binary.LittleEndian, int64(entry.RestatMtime))
	binary.Write(w.buffer, binary.LittleEndian, entry.CommandHash)
	binary.Write(w.buffer, binary.LittleEndian, uint32(len(entry.Output)))
	w.buffer.WriteString(entry.Output)
	
	// Write to file
	_, err := w.file.Write(w.buffer.Bytes())
	return err
}

// Close closes the binary writer
func (w *BinaryWriter) Close() error {
	return w.file.Close()
}