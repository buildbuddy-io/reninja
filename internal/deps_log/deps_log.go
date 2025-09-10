package deps_log

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/buildbuddy-io/gin/internal/graph"
)

const (
	// DepsLogVersion is the current deps log format version
	DepsLogVersion = 4

	// DepsLogSignature for binary format
	DepsLogSignature = 0x6e696e44 // "ninD"
)

// DepsRecord represents a dependency record
type DepsRecord struct {
	Output       *graph.Node
	Dependencies []*graph.Node
	Mtime        graph.TimeStamp
}

// DepsLog tracks dependency information
type DepsLog struct {
	mu      sync.RWMutex
	records map[*graph.Node]*DepsRecord
	logFile string
	dirty   bool
	nodeMap map[string]*graph.Node
}

// NewDepsLog creates a new DepsLog
func NewDepsLog() *DepsLog {
	return &DepsLog{
		records: make(map[*graph.Node]*DepsRecord),
		nodeMap: make(map[string]*graph.Node),
	}
}

// OpenForWrite opens the deps log for writing
func (d *DepsLog) OpenForWrite(path string, buildDir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.logFile = path

	// Load existing log if it exists
	if err := d.load(buildDir); err != nil {
		// If loading fails, start fresh
		d.records = make(map[*graph.Node]*DepsRecord)
	}

	return nil
}

// RecordDeps records dependencies for a node
func (d *DepsLog) RecordDeps(output *graph.Node, mtime graph.TimeStamp, deps []*graph.Node) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if we already have this exact record
	if existing, ok := d.records[output]; ok {
		if existing.Mtime == mtime && d.depsEqual(existing.Dependencies, deps) {
			return true // No change needed
		}
	}

	record := &DepsRecord{
		Output:       output,
		Dependencies: make([]*graph.Node, len(deps)),
		Mtime:        mtime,
	}
	copy(record.Dependencies, deps)

	d.records[output] = record
	d.dirty = true

	return true
}

// GetDeps returns the recorded dependencies for a node
func (d *DepsLog) GetDeps(node *graph.Node) *DepsRecord {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return d.records[node]
}

// Save writes the deps log to disk
func (d *DepsLog) Save() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.dirty || d.logFile == "" {
		return nil
	}

	// Write to temp file first for atomic update
	tempFile := d.logFile + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(file)

	// Write header (signature and version)
	if err := binary.Write(writer, binary.LittleEndian, uint32(DepsLogSignature)); err != nil {
		file.Close()
		os.Remove(tempFile)
		return err
	}

	if err := binary.Write(writer, binary.LittleEndian, uint32(DepsLogVersion)); err != nil {
		file.Close()
		os.Remove(tempFile)
		return err
	}

	// Write records
	for _, record := range d.records {
		// Write output path
		outputPath := record.Output.Path()
		if err := d.writeString(writer, outputPath); err != nil {
			file.Close()
			os.Remove(tempFile)
			return err
		}

		// Write mtime
		if err := binary.Write(writer, binary.LittleEndian, int64(record.Mtime)); err != nil {
			file.Close()
			os.Remove(tempFile)
			return err
		}

		// Write dependency count
		if err := binary.Write(writer, binary.LittleEndian, uint32(len(record.Dependencies))); err != nil {
			file.Close()
			os.Remove(tempFile)
			return err
		}

		// Write dependencies
		for _, dep := range record.Dependencies {
			if err := d.writeString(writer, dep.Path()); err != nil {
				file.Close()
				os.Remove(tempFile)
				return err
			}
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
	if err := os.Rename(tempFile, d.logFile); err != nil {
		os.Remove(tempFile)
		return err
	}

	d.dirty = false
	return nil
}

// load reads the deps log from disk
func (d *DepsLog) load(buildDir string) error {
	if d.logFile == "" {
		return nil
	}

	file, err := os.Open(d.logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing log
		}
		return err
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	// Read header
	var signature, version uint32
	if err := binary.Read(reader, binary.LittleEndian, &signature); err != nil {
		if err == io.EOF {
			return nil // Empty file
		}
		return fmt.Errorf("failed to read signature: %w", err)
	}

	if signature != DepsLogSignature {
		return fmt.Errorf("invalid deps log signature: %x", signature)
	}

	if err := binary.Read(reader, binary.LittleEndian, &version); err != nil {
		return fmt.Errorf("failed to read version: %w", err)
	}

	if version != DepsLogVersion {
		return fmt.Errorf("unsupported deps log version: %d", version)
	}

	// Read records
	for {
		// Read output path
		outputPath, err := d.readString(reader)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Get or create node
		outputNode := d.getOrCreateNode(outputPath)

		// Read mtime
		var mtime int64
		if err := binary.Read(reader, binary.LittleEndian, &mtime); err != nil {
			return err
		}

		// Read dependency count
		var depCount uint32
		if err := binary.Read(reader, binary.LittleEndian, &depCount); err != nil {
			return err
		}

		// Read dependencies
		deps := make([]*graph.Node, depCount)
		for i := uint32(0); i < depCount; i++ {
			depPath, err := d.readString(reader)
			if err != nil {
				return err
			}
			deps[i] = d.getOrCreateNode(depPath)
		}

		// Store record
		d.records[outputNode] = &DepsRecord{
			Output:       outputNode,
			Dependencies: deps,
			Mtime:        graph.TimeStamp(mtime),
		}
	}

	return nil
}

// writeString writes a string with length prefix
func (d *DepsLog) writeString(w io.Writer, s string) error {
	if err := binary.Write(w, binary.LittleEndian, uint32(len(s))); err != nil {
		return err
	}
	_, err := w.Write([]byte(s))
	return err
}

// readString reads a string with length prefix
func (d *DepsLog) readString(r io.Reader) (string, error) {
	var length uint32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return "", err
	}

	if length > 1024*1024 { // Sanity check: 1MB max path
		return "", fmt.Errorf("path too long: %d bytes", length)
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}

	return string(buf), nil
}

// getOrCreateNode gets or creates a node for tracking
func (d *DepsLog) getOrCreateNode(path string) *graph.Node {
	if node, ok := d.nodeMap[path]; ok {
		return node
	}

	// Create a minimal node for tracking
	// We use NewNode which takes the path and slashBits (0 for Unix)
	node := graph.NewNode(path, 0)
	d.nodeMap[path] = node
	return node
}

// depsEqual checks if two dependency lists are equal
func (d *DepsLog) depsEqual(a, b []*graph.Node) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i].Path() != b[i].Path() {
			return false
		}
	}

	return true
}

// Clear removes all records
func (d *DepsLog) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.records = make(map[*graph.Node]*DepsRecord)
	d.nodeMap = make(map[string]*graph.Node)
	d.dirty = true
}

// IsDirty returns whether the log has unsaved changes
func (d *DepsLog) IsDirty() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.dirty
}

// GetLogPath returns the log file path
func (d *DepsLog) GetLogPath() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.logFile
}

// Stats represents deps log statistics
type DepsStats struct {
	TotalRecords int
	TotalDeps    int
}

// GetStats returns statistics about the deps log
func (d *DepsLog) GetStats() *DepsStats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	stats := &DepsStats{
		TotalRecords: len(d.records),
	}

	for _, record := range d.records {
		stats.TotalDeps += len(record.Dependencies)
	}

	return stats
}

// PruneMissing removes records for nodes that no longer exist
func (d *DepsLog) PruneMissing() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	removed := 0
	for node, record := range d.records {
		// Check if output still exists
		if _, err := os.Stat(node.Path()); os.IsNotExist(err) {
			delete(d.records, node)
			removed++
			d.dirty = true
			continue
		}

		// Check if any dependencies are missing
		validDeps := make([]*graph.Node, 0, len(record.Dependencies))
		for _, dep := range record.Dependencies {
			if _, err := os.Stat(dep.Path()); err == nil {
				validDeps = append(validDeps, dep)
			}
		}

		if len(validDeps) != len(record.Dependencies) {
			record.Dependencies = validDeps
			d.dirty = true
		}
	}

	return removed
}
