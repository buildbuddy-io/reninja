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

package disk

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buildbuddy-io/gin/internal/timestamp"
)

// Interface provides file system operations
type Interface interface {
	Stat(path string) (timestamp.TimeStamp, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, contents []byte) error
	MakeDir(path string) error
	RemoveFile(path string) error
}

// RealDiskInterface implements DiskInterface for real file system operations
type RealDiskInterface struct {
	// Cache for stat results to avoid repeated system calls
	statCache map[string]statCacheEntry
}

type statCacheEntry struct {
	mtime  timestamp.TimeStamp
	exists bool
	cached time.Time
}

// NewRealDiskInterface creates a new RealDiskInterface
func NewRealDiskInterface() *RealDiskInterface {
	return &RealDiskInterface{
		statCache: make(map[string]statCacheEntry),
	}
}

// Stat returns the modification time of a file
func (d *RealDiskInterface) Stat(path string) (timestamp.TimeStamp, error) {
	// Check cache first
	if entry, ok := d.statCache[path]; ok {
		// Cache entries are valid for a short time during a build
		if time.Since(entry.cached) < 1*time.Second {
			if !entry.exists {
				return timestamp.TimeStampMissing, os.ErrNotExist
			}
			return entry.mtime, nil
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Cache the non-existence
			d.statCache[path] = statCacheEntry{
				mtime:  timestamp.TimeStampMissing,
				exists: false,
				cached: time.Now(),
			}
			return timestamp.TimeStampMissing, err
		}
		return timestamp.TimeStampUnknown, err
	}

	// Convert to milliseconds since epoch
	mtime := timestamp.TimeStamp(info.ModTime().UnixMilli())

	// Cache the result
	d.statCache[path] = statCacheEntry{
		mtime:  mtime,
		exists: true,
		cached: time.Now(),
	}

	return mtime, nil
}

// ReadFile reads the contents of a file
func (d *RealDiskInterface) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile writes contents to a file
func (d *RealDiskInterface) WriteFile(path string, contents []byte) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, contents, 0644)
}

// MakeDir creates a directory and all necessary parents
func (d *RealDiskInterface) MakeDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// RemoveFile removes a file
func (d *RealDiskInterface) RemoveFile(path string) error {
	// Invalidate cache
	delete(d.statCache, path)
	return os.Remove(path)
}

// ClearStatCache clears the stat cache
func (d *RealDiskInterface) ClearStatCache() {
	d.statCache = make(map[string]statCacheEntry)
}

// MockDiskInterface provides a mock implementation for testing
type MockDiskInterface struct {
	files     map[string]mockFile
	filesRead []string
	now       timestamp.TimeStamp
}

type mockFile struct {
	contents []byte
	mtime    timestamp.TimeStamp
}

// NewMockDiskInterface creates a new MockDiskInterface
func NewMockDiskInterface() *MockDiskInterface {
	return &MockDiskInterface{
		files:     make(map[string]mockFile),
		filesRead: make([]string, 0),
		now:       timestamp.TimeStamp(time.Now().UnixMilli()),
	}
}

// Stat returns the modification time of a mock file
func (m *MockDiskInterface) Stat(path string) (timestamp.TimeStamp, error) {
	if file, ok := m.files[path]; ok {
		return file.mtime, nil
	}
	return timestamp.TimeStampMissing, os.ErrNotExist
}

// ReadFile reads the contents of a mock file
func (m *MockDiskInterface) ReadFile(path string) ([]byte, error) {
	m.filesRead = append(m.filesRead, path)
	if file, ok := m.files[path]; ok {
		return file.contents, nil
	}
	return nil, os.ErrNotExist
}

// WriteFile writes contents to a mock file
func (m *MockDiskInterface) WriteFile(path string, contents []byte) error {
	m.files[path] = mockFile{
		contents: contents,
		mtime:    m.now,
	}
	return nil
}

// MakeDir creates a mock directory
func (m *MockDiskInterface) MakeDir(path string) error {
	// Just mark it as existing with no contents
	m.files[path+"/"] = mockFile{
		mtime: m.now,
	}
	return nil
}

// RemoveFile removes a mock file
func (m *MockDiskInterface) RemoveFile(path string) error {
	if _, ok := m.files[path]; !ok {
		return os.ErrNotExist
	}
	delete(m.files, path)
	return nil
}

// AddFile adds a file to the mock file system
func (m *MockDiskInterface) AddFile(path string, contents []byte, mtime timestamp.TimeStamp) {
	m.files[path] = mockFile{
		contents: contents,
		mtime:    mtime,
	}
}

func (m *MockDiskInterface) FilesRead() []string {
	return m.filesRead
}

func (m *MockDiskInterface) Tick() timestamp.TimeStamp {
	t2 := m.now + 1
	m.now = t2
	return t2
}

// FileReader provides an interface for reading files
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// IsNotExist checks if an error indicates a file doesn't exist
func IsNotExist(err error) bool {
	return os.IsNotExist(err)
}

// NormalizePath normalizes a file path for the current platform
func NormalizePath(path string) string {
	// Convert forward slashes to native separator
	path = filepath.FromSlash(path)

	// Clean up the path
	path = filepath.Clean(path)

	return path
}

// IsAbsPath checks if a path is absolute
func IsAbsPath(path string) bool {
	return filepath.IsAbs(path)
}

// JoinPath joins path elements
func JoinPath(elem ...string) string {
	return filepath.Join(elem...)
}

// GetDirName returns the directory part of a path
func GetDirName(path string) string {
	return filepath.Dir(path)
}

// GetBaseName returns the base name of a path
func GetBaseName(path string) string {
	return filepath.Base(path)
}

// GetExtension returns the file extension
func GetExtension(path string) string {
	return filepath.Ext(path)
}

// RelativePath returns a relative path from base to target
func RelativePath(base, target string) (string, error) {
	return filepath.Rel(base, target)
}

// CopyFile copies a file from src to dst
func CopyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Create destination directory if needed
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	// Copy file mode and modification time
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	err = os.Chmod(dst, sourceInfo.Mode())
	if err != nil {
		return err
	}

	return os.Chtimes(dst, time.Now(), sourceInfo.ModTime())
}

// ListDirectory lists files in a directory
func ListDirectory(path string) ([]string, error) {
	entries, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}

	return files, nil
}

// DirectoryExists checks if a directory exists
func DirectoryExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// MakeDirs creates a directory and all necessary parents
func MakeDirs(path string) error {
	return os.MkdirAll(path, 0755)
}

// RemoveAll removes a file or directory and all its contents
func RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// GetWorkingDirectory returns the current working directory
func GetWorkingDirectory() (string, error) {
	return os.Getwd()
}

// ChangeDirectory changes the current working directory
func ChangeDirectory(path string) error {
	return os.Chdir(path)
}

// GetTempDir returns the system temporary directory
func GetTempDir() string {
	return os.TempDir()
}

// CreateTempFile creates a temporary file
func CreateTempFile(dir, pattern string) (*os.File, error) {
	return ioutil.TempFile(dir, pattern)
}

// CreateTempDir creates a temporary directory
func CreateTempDir(dir, pattern string) (string, error) {
	return ioutil.TempDir(dir, pattern)
}

// GetFileSize returns the size of a file
func GetFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// TouchFile updates the modification time of a file
func TouchFile(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// IsSymlink checks if a path is a symbolic link
func IsSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// CreateSymlink creates a symbolic link
func CreateSymlink(target, link string) error {
	return os.Symlink(target, link)
}

// ReadSymlink reads the target of a symbolic link
func ReadSymlink(path string) (string, error) {
	return os.Readlink(path)
}

// GetAbsolutePath returns the absolute path
func GetAbsolutePath(path string) (string, error) {
	return filepath.Abs(path)
}

// SplitPath splits a path into directory and file components
func SplitPath(path string) (dir, file string) {
	return filepath.Split(path)
}

// MatchPattern checks if a path matches a glob pattern
func MatchPattern(pattern, path string) (bool, error) {
	return filepath.Match(pattern, path)
}

// GlobFiles returns files matching a glob pattern
func GlobFiles(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}

// WriteFileAtomic writes a file atomically by writing to a temp file and renaming
func WriteFileAtomic(path string, contents []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Create temp file in same directory for atomic rename
	temp, err := ioutil.TempFile(dir, ".tmp-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()

	// Clean up temp file on error
	defer func() {
		if temp != nil {
			temp.Close()
			os.Remove(tempPath)
		}
	}()

	// Write contents
	if _, err := temp.Write(contents); err != nil {
		return err
	}

	// Sync to disk
	if err := temp.Sync(); err != nil {
		return err
	}

	// Close before rename
	if err := temp.Close(); err != nil {
		return err
	}
	temp = nil // Prevent cleanup

	// Atomic rename
	return os.Rename(tempPath, path)
}

// EscapePath escapes a path for use in shell commands
func EscapePath(path string) string {
	if strings.ContainsAny(path, " \t\n'\"\\$") {
		return fmt.Sprintf("'%s'", strings.ReplaceAll(path, "'", "'\\''"))
	}
	return path
}
