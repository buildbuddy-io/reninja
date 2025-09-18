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

package graph

import (
	"fmt"
	"strings"

	"github.com/buildbuddy-io/gin/internal/disk"
	"github.com/buildbuddy-io/gin/internal/timestamp"
)

// ExistenceStatus represents the existence state of a file
type ExistenceStatus int

const (
	// ExistenceStatusUnknown means the file hasn't been examined
	ExistenceStatusUnknown ExistenceStatus = iota
	// ExistenceStatusMissing means the file doesn't exist
	ExistenceStatusMissing
	// ExistenceStatusExists means the path is an actual file
	ExistenceStatusExists
)

// Node represents a file in the dependency graph
type Node struct {
	path      string
	slashBits uint64 // Tracks normalized backslashes for Windows compatibility

	// Possible values of mtime:
	//   -1: file hasn't been examined
	//   0:  we looked, and file doesn't exist
	//   >0: actual file's mtime, or the latest mtime of its dependencies if it doesn't exist
	mtime  timestamp.TimeStamp
	exists ExistenceStatus

	// Dirty is true when the underlying file is out-of-date
	dirty bool

	// Store whether dyndep information is expected from this node but
	// has not yet been loaded
	dyndepPending bool

	// Set to true when this node comes from a depfile, a dyndep file or the
	// deps log. If it does not have a producing edge, the build should not
	// abort if it is missing
	generatedByDepLoader bool

	// The Edge that produces this Node, or nil when there is no
	// known edge to produce it
	inEdge *Edge

	// All Edges that use this Node as an input
	outEdges []*Edge

	// All Edges that use this Node as a validation
	validationOutEdges []*Edge

	// A dense integer id for the node, assigned and used by DepsLog
	id int
}

// NewNode creates a new Node with the given path
func NewNode(path string, slashBits uint64) *Node {
	return &Node{
		path:                 path,
		slashBits:            slashBits,
		mtime:                timestamp.TimeStampUnknown,
		exists:               ExistenceStatusUnknown,
		dirty:                false,
		dyndepPending:        false,
		generatedByDepLoader: true, // Default to true like C++ version
		id:                   -1,
	}
}

func (n *Node) Dump(prefix string) {
	existStr := " (:missing)"
	if n.Exists() {
		existStr = ""
	}

	dirtyStr := " clean"
	if n.Dirty() {
		dirtyStr = " dirty"
	}

	fmt.Printf("%s <%s %p> mtime: %d %s, (:%s), ", prefix, n.path, n, n.mtime, existStr, dirtyStr)
	if in := n.InEdge(); in != nil {
		in.Dump("in-edge: ")
	} else {
		fmt.Printf("no in-edge\n")
	}

	fmt.Printf(" out edges:\n")
	for _, e := range n.OutEdges() {
		e.Dump(" +- ")
	}

	if validationOutEdges := n.ValidationOutEdges(); len(validationOutEdges) > 0 {
		fmt.Printf(" validation out edges:\n")
		for _, e := range validationOutEdges {
			e.Dump(" +- ")
		}
	}
}

// Path returns the node's path
func (n *Node) Path() string {
	return n.path
}

// PathDecanonicalized returns the path with original slash styles restored
func (n *Node) PathDecanonicalized() string {
	return pathDecanonicalized(n.path, n.slashBits)
}

// pathDecanonicalized converts forward slashes back to backslashes based on slashBits
func pathDecanonicalized(path string, slashBits uint64) string {
	if slashBits == 0 {
		return path
	}

	result := strings.Builder{}
	result.Grow(len(path))

	bit := uint64(1)
	for _, ch := range path {
		if ch == '/' && (slashBits&bit) != 0 {
			result.WriteByte('\\')
		} else {
			result.WriteRune(ch)
		}
		if ch == '/' || ch == '\\' {
			bit <<= 1
		}
	}

	return result.String()
}

// SlashBits returns the slash normalization bits
func (n *Node) SlashBits() uint64 {
	return n.slashBits
}

// Mtime returns the modification time
func (n *Node) Mtime() timestamp.TimeStamp {
	return n.mtime
}

// SetMtime sets the modification time
func (n *Node) SetMtime(mtime timestamp.TimeStamp) {
	n.mtime = mtime
}

// Exists returns true if the file exists
func (n *Node) Exists() bool {
	return n.exists == ExistenceStatusExists
}

// StatusKnown returns true if the file's existence status has been checked
func (n *Node) StatusKnown() bool {
	return n.exists != ExistenceStatusUnknown
}

// Dirty returns whether the node is dirty
func (n *Node) Dirty() bool {
	return n.dirty
}

// SetDirty sets the dirty state
func (n *Node) SetDirty(dirty bool) {
	n.dirty = dirty
}

// MarkDirty marks the node as dirty
func (n *Node) MarkDirty() {
	n.dirty = true
}

// DyndepPending returns whether dyndep is pending
func (n *Node) DyndepPending() bool {
	return n.dyndepPending
}

// SetDyndepPending sets the dyndep pending state
func (n *Node) SetDyndepPending(pending bool) {
	n.dyndepPending = pending
}

// InEdge returns the edge that produces this node
func (n *Node) InEdge() *Edge {
	return n.inEdge
}

// SetInEdge sets the edge that produces this node
func (n *Node) SetInEdge(edge *Edge) {
	n.inEdge = edge
}

// GeneratedByDepLoader indicates whether this node was generated from a depfile or dyndep file
func (n *Node) GeneratedByDepLoader() bool {
	return n.generatedByDepLoader
}

// SetGeneratedByDepLoader sets whether this node was generated from a depfile or dyndep file
func (n *Node) SetGeneratedByDepLoader(value bool) {
	n.generatedByDepLoader = value
}

// ID returns the node's ID
func (n *Node) ID() int {
	return n.id
}

// SetID sets the node's ID
func (n *Node) SetID(id int) {
	n.id = id
}

// OutEdges returns all edges that use this node as an input
func (n *Node) OutEdges() []*Edge {
	return n.outEdges
}

// ValidationOutEdges returns all edges that use this node as a validation
func (n *Node) ValidationOutEdges() []*Edge {
	return n.validationOutEdges
}

// AddOutEdge adds an edge that uses this node as an input
func (n *Node) AddOutEdge(edge *Edge) {
	n.outEdges = append(n.outEdges, edge)
}

// AddValidationOutEdge adds an edge that uses this node as a validation
func (n *Node) AddValidationOutEdge(edge *Edge) {
	n.validationOutEdges = append(n.validationOutEdges, edge)
}

// ResetState marks as not-yet-stat()ed and not dirty
func (n *Node) ResetState() {
	n.mtime = timestamp.TimeStampUnknown
	n.exists = ExistenceStatusUnknown
	n.dirty = false
}

// MarkMissing marks the Node as already-stat()ed and missing
func (n *Node) MarkMissing() {
	if n.mtime == timestamp.TimeStampUnknown {
		n.mtime = timestamp.TimeStampMissing
	}
	n.exists = ExistenceStatusMissing
}

// UpdatePhonyMtime updates mtime for phony targets
func (n *Node) UpdatePhonyMtime(mtime timestamp.TimeStamp) {
	if !n.Exists() {
		n.mtime = mtime
	}
}

// Stat updates the node's status from disk
func (n *Node) Stat(diskInterface disk.Interface) error {
	mtime, err := diskInterface.Stat(n.path)
	if err != nil {
		if IsNotExist(err) {
			n.mtime = timestamp.TimeStampMissing
			n.exists = ExistenceStatusMissing
			return nil
		}
		return err
	}

	n.mtime = mtime
	n.exists = ExistenceStatusExists
	return nil
}

// StatIfNecessary stats the file if not already done
func (n *Node) StatIfNecessary(diskInterface disk.Interface) error {
	if n.StatusKnown() {
		return nil
	}
	return n.Stat(diskInterface)
}

// IsNotExist checks if an error indicates a file doesn't exist
func IsNotExist(err error) bool {
	// This will be implemented based on the disk interface
	// For now, we'll check for standard Go file not found errors
	return strings.Contains(err.Error(), "no such file") ||
		strings.Contains(err.Error(), "cannot find the file") ||
		strings.Contains(err.Error(), "file does not exist")
}
