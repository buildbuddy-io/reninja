package deps_log

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/buildbuddy-io/gin/internal/graph"
	"github.com/buildbuddy-io/gin/internal/state"
	"github.com/buildbuddy-io/gin/internal/timestamp"
)

const (
	// The version is stored as 4 bytes after the signature and also serves as a
	// byte order mark. Signature and version combined are 16 bytes long.
	fileSignature  = "# ninjadeps\n"
	currentVersion = 4

	// Record size is currently limited to less than the full 32 bit, due to
	// internal buffers having to have this size.
	maxRecordSize = (1 << 19) - 1
)

type Deps struct {
	Mtime timestamp.TimeStamp
	Nodes []*graph.Node
}

func NewDeps(mtime timestamp.TimeStamp, nodeCount int) *Deps {
	return &Deps{
		Mtime: mtime,
		Nodes: make([]*graph.Node, nodeCount),
	}
}

// DepsLog tracks dependency information
type DepsLog struct {
	nodes []*graph.Node
	deps  []*Deps

	logFile           *os.File
	logFilePath       string
	needsRecompaction bool
}

// NewDepsLog creates a new DepsLog
func NewDepsLog() *DepsLog {
	return &DepsLog{
		nodes:             make([]*graph.Node, 0),
		deps:              make([]*Deps, 0),
		needsRecompaction: false,
	}
}

func (d *DepsLog) OpenForWrite(path string) error {
	if d.needsRecompaction {
		if err := d.Recompact(path); err != nil {
			return err
		}
	}
	if d.logFile != nil {
		panic("logFile was already opened!")
	}

	// we don't actually open the file right now, but will
	// do so on the first write attempt
	d.logFilePath = path
	return nil
}

func (d *DepsLog) RecordId(node *graph.Node) error {
	pathSize := len(node.Path())
	if pathSize <= 0 {
		panic("Trying to record empty path Node!")
	}
	padding := (4 - pathSize%4) % 4 // Pad path to 4 byte boundary
	size := pathSize + padding + 4

	if size > maxRecordSize {
		return fmt.Errorf("ERANGE")
	}

	if err := d.OpenForWriteIfNeeded(); err != nil {
		return err
	}
	// TODO(tylerw): this pattern is used a lot in this file and could probably be
	// simplified by adding a method like `fWrite4ByteInt(i int) error`.
	sizeBuf := intTo4Bytes(size)
	if _, err := d.logFile.Write(sizeBuf); err != nil {
		return err
	}
	pathBuf := make([]byte, pathSize)
	copy(pathBuf, []byte(node.Path()))
	if _, err := d.logFile.Write(pathBuf); err != nil {
		return err
	}
	if padding > 0 {
		if _, err := d.logFile.Write(make([]byte, padding)); err != nil {
			return err
		}
	}
	id := len(d.nodes)
	checksum := ^id
	checksumBuf := intTo4Bytes(checksum)
	if _, err := d.logFile.Write(checksumBuf); err != nil {
		return err
	}
	node.SetID(id)
	d.nodes = append(d.nodes, node)
	return nil
}

func (d *DepsLog) UpdateDeps(outID int, deps *Deps) bool {
	if outID >= len(d.deps) {
		newCapacity := outID + 1
		newSlice := make([]*Deps, newCapacity)
		copy(newSlice[:len(d.deps)], d.deps)
		d.deps = newSlice
	}
	deleteOld := d.deps[outID] != nil
	if deleteOld {
		d.deps[outID] = nil
	}
	d.deps[outID] = deps
	return deleteOld
}

func (d *DepsLog) GetDeps(node *graph.Node) *Deps {
	// Abort if the node has no id (never referenced in the deps) or if
	// there's no deps recorded for the node.
	if node.ID() < 0 || node.ID() >= len(d.deps) {
		return nil
	}
	return d.deps[node.ID()]
}

func (d *DepsLog) GetFirstReverseDepsNode(node *graph.Node) *graph.Node {
	for i := range d.deps {
		deps := d.deps[i]
		if deps == nil {
			continue
		}
		for j := range deps.Nodes {
			if deps.Nodes[j].Path() == node.Path() {
				return d.nodes[i]
			}
		}
	}
	return nil
}

func (d *DepsLog) RecordDeps(node *graph.Node, mtime timestamp.TimeStamp, nodes []*graph.Node) error {
	// Track whether there's any new data to be recorded.
	nodeCount := len(nodes)
	madeChange := false

	// Assign ids to all nodes that are missing one.
	if node.ID() < 0 {
		if err := d.RecordId(node); err != nil {
			return err
		}
		madeChange = true
	}
	for i := range nodes {
		if nodes[i].ID() < 0 {
			if err := d.RecordId(nodes[i]); err != nil {
				return err
			}
			madeChange = true
		}
	}

	if !madeChange {
		deps := d.GetDeps(node)
		if deps == nil || deps.Mtime != mtime || len(deps.Nodes) != nodeCount {
			madeChange = true
		} else {
			for i := range nodes {
				if deps.Nodes[i] != nodes[i] {
					madeChange = true
					break
				}
			}
		}
	}
	if !madeChange {
		return nil
	}

	size := 4 * (1 + 2 + nodeCount)
	if size > maxRecordSize {
		return fmt.Errorf("ERANGE")
	}
	if err := d.OpenForWriteIfNeeded(); err != nil {
		return err
	}
	size |= 0x80000000
	sizeBuf := intTo4Bytes(size)
	if _, err := d.logFile.Write(sizeBuf); err != nil {
		return err
	}
	id := node.ID()
	idBuf := intTo4Bytes(id)
	if _, err := d.logFile.Write(idBuf); err != nil {
		return err
	}
	mtimePart := int(int64(mtime) & 0xffffffff)
	mtimeBuf := intTo4Bytes(mtimePart)
	if _, err := d.logFile.Write(mtimeBuf); err != nil {
		return err
	}
	mtimePart = int(int64(mtime>>32) & 0xffffffff)
	mtimeBuf = intTo4Bytes(mtimePart)
	if _, err := d.logFile.Write(mtimeBuf); err != nil {
		return err
	}
	for i := range nodes {
		idBuf := intTo4Bytes(nodes[i].ID())
		if _, err := d.logFile.Write(idBuf); err != nil {
			return err
		}
	}

	deps := NewDeps(mtime, nodeCount)
	for i := range nodes {
		deps.Nodes[i] = nodes[i]
	}
	d.UpdateDeps(node.ID(), deps)

	return nil
}

func (d *DepsLog) Load(path string, state *state.State) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	buf := make([]byte, maxRecordSize)

	n, err := io.ReadFull(f, buf[:len(fileSignature)])
	validHeader := err == nil && n == len(fileSignature) &&
		string(buf[:len(fileSignature)]) == fileSignature

	n, err = io.ReadFull(f, buf[:4])
	version := bytesToInt(buf[:4])
	validVersion := err == nil && n == 4 && version == currentVersion

	if !validHeader || !validVersion {
		if version == 1 {
			return fmt.Errorf("deps log version change; rebuilding")
		} else {
			return fmt.Errorf("bad deps log signature or version; starting over")
		}
		f.Close()
		os.Remove(path)
		return nil
	}

	offset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		f.Close()
		return err
	}
	readFailed := false
	uniqueDepRecordCount := 0
	totalDepRecordCount := 0
	for {
		n, err = io.ReadFull(f, buf[:4])
		if err != nil || n != 4 {
			readFailed = true
			break
		}
		size := bytesToInt(buf[:4])
		isDeps := (size >> 31) != 0
		size = size & 0x7FFFFFFF

		if size > maxRecordSize {
			readFailed = true
			break
		}
		n, err = io.ReadFull(f, buf[:size])
		if err != nil || n != size {
			readFailed = true
			break
		}
		offset += int64(size + 4 /*=sizeof(size)*/)

		if isDeps {
			if size%4 != 0 {
				readFailed = true
				break
			}
			outID := bytesToInt(buf[0:4])
			mtimeLow := bytesToInt(buf[4:8])
			mtimeHigh := bytesToInt(buf[8:12])
			mtime := timestamp.TimeStamp(mtimeLow | (mtimeHigh << 32))

			depsCount := (size / 4) - 3
			deps := NewDeps(mtime, depsCount)

			for i := 0; i < depsCount; i++ {
				s := 12 + (4 * i)
				e := 12 + (4 * i) + 4
				nodeID := bytesToInt(buf[s:e])
				if nodeID >= len(d.nodes) || d.nodes[nodeID] == nil {
					readFailed = true
					break
				}
				deps.Nodes[i] = d.nodes[nodeID]
			}
			totalDepRecordCount++
			if !d.UpdateDeps(outID, deps) {
				uniqueDepRecordCount++
			}
		} else {
			pathSize := size - 4
			if pathSize <= 0 {
				readFailed = true
				break
			}
			if buf[pathSize-1] == 0 {
				pathSize -= 1
			}
			if buf[pathSize-1] == 0 {
				pathSize -= 1
			}
			if buf[pathSize-1] == 0 {
				pathSize -= 1
			}
			subPath := string(buf[:pathSize])
			node := state.GetNode(subPath)

			checksum := bytesToInt(buf[size-4 : size])
			expectedID := ^checksum
			id := len(d.nodes)
			if id != expectedID || node.ID() >= 0 {
				readFailed = true
				break
			}
			node.SetID(id)
			d.nodes = append(d.nodes, node)
		}
	}
	if readFailed {
		f.Close()

		if err := os.Truncate(path, offset); err != nil {
			return err
		}
		return nil
	}
	f.Close()
	minCompactionEntryCount := 1000
	compactionRatio := 3

	if totalDepRecordCount > minCompactionEntryCount &&
		totalDepRecordCount > uniqueDepRecordCount*compactionRatio {
		d.needsRecompaction = true
	}

	return nil
}

func (d *DepsLog) Recompact(path string) error {
	if err := d.Close(); err != nil {
		return err
	}
	tempPath := path + ".recompact"

	// OpenForWrite() opens for append.  Make sure it's not appending to a
	// left-over file from a previous recompaction attempt that crashed somehow.
	_ = os.Remove(tempPath) // ignore error

	newLog := NewDepsLog()
	if err := newLog.OpenForWrite(tempPath); err != nil {
		return err
	}
	for i := range d.nodes {
		d.nodes[i].SetID(-1)
	}

	for oldID := range d.deps {
		deps := d.deps[oldID]
		if deps == nil {
			continue
		}
		if !d.IsDepsEntryLiveFor(d.nodes[oldID]) {
			continue
		}
		if err := newLog.RecordDeps(d.nodes[oldID], deps.Mtime, deps.Nodes); err != nil {
			newLog.Close()
			return err
		}
	}

	newLog.Close()

	d.deps = newLog.deps
	d.nodes = newLog.nodes

	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func (d *DepsLog) IsDepsEntryLiveFor(node *graph.Node) bool {
	// Skip entries that don't have in-edges or whose edges don't have a
	// "deps" attribute. They were in the deps log from previous builds, but
	// the the files they were for were removed from the build and their deps
	// entries are no longer needed.
	// (Without the check for "deps", a chain of two or more nodes that each
	// had deps wouldn't be collected in a single recompaction.)
	return node.InEdge() != nil && node.InEdge().GetBinding("deps") != ""
}

func (d *DepsLog) Close() error {
	if err := d.OpenForWriteIfNeeded(); err != nil {
		return err
	}
	if d.logFile != nil {
		if err := d.logFile.Close(); err != nil {
			return err
		}
	}
	d.logFile = nil
	return nil
}

func (d *DepsLog) OpenForWriteIfNeeded() error {
	if d.logFile != nil || len(d.logFilePath) == 0 {
		return nil
	}
	f, err := os.OpenFile(d.logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	d.logFile = f

	pos, err := d.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if pos == 0 {
		if _, err := d.logFile.Write([]byte(fileSignature)); err != nil {
			return err
		}
		versionBuf := intTo4Bytes(currentVersion)
		if _, err := d.logFile.Write(versionBuf); err != nil {
			return err
		}
	}
	d.logFilePath = "" // TODO(tylerw): is this necessary? Not present in build_log.go
	return nil
}

func (d *DepsLog) TestingGetNodes() []*graph.Node {
	return d.nodes
}

func (d *DepsLog) TestingGetDeps() []*Deps {
	return d.deps
}

func intTo4Bytes(i int) []byte {
	i2 := int32(i)
	buf := make([]byte, binary.Size(i2))
	binary.Encode(buf, binary.NativeEndian, i2)
	return buf
}

func bytesToInt(buf []byte) int {
	var i int32
	binary.Decode(buf, binary.NativeEndian, &i)
	return int(i)
}
