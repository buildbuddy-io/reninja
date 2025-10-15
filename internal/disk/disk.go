package disk

import (
	"fmt"
	"io"
	"io/ioutil"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/buildbuddy-io/gin/internal/timestamp"
)

// Interface provides file system operations
type Interface interface {
	// Read and store in given string.  On success, return Okay.
	// On error, return another Status and fill |err|.
	ReadFile(path string) ([]byte, error)

	// stat() a file, returning the mtime, or 0 if missing and -1 on
	// other errors.
	Stat(path string) (timestamp.TimeStamp, error)

	// Create a directory, returning false on failure.
	MakeDir(path string) error

	// Create a file, with the specified name and contents
	// If \a crlf_on_windows is true, \n will be converted to \r\n (only on
	// Windows builds of Ninja).
	// Returns true on success, false on failure
	WriteFile(path string, contents []byte, crlfOnWindows bool) error

	// Remove the file named @a path. It behaves like 'rm -f path' so no errors
	// are reported if it does not exists.
	// @returns 0 if the file has been removed,
	//          1 if the file does not exist, and
	//          -1 if an error occurs.
	RemoveFile(path string) int

	// Create all the parent directories for path; like mkdir -p
	// `basename path`.
	MakeDirs(path string) error
}

type RealDiskInterface struct{}

func NewRealDiskInterface() *RealDiskInterface {
	return &RealDiskInterface{}
}

func (d *RealDiskInterface) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (d *RealDiskInterface) Stat(path string) (timestamp.TimeStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return timestamp.TimeStampMissing, nil
		}
		return timestamp.TimeStampUnknown, err
	}
	mtime := timestamp.TimeStamp(info.ModTime().UnixMilli())
	return mtime, nil
}

func (d *RealDiskInterface) MakeDir(path string) error {
	err := os.Mkdir(path, 0755)
	if err != nil && os.IsExist(err) {
		return nil
	}
	return err
}

func (d *RealDiskInterface) Create(path string, contents []byte) error {
	return d.WriteFile(path, contents, false)
}

func (d *RealDiskInterface) WriteFile(path string, contents []byte, crlfOnWindows bool) error {
	// TODO(tylerw): windows support do something.
	return os.WriteFile(path, contents, 0644)
}

func (d *RealDiskInterface) MakeDirs(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0755)
}

func (d *RealDiskInterface) RemoveFile(path string) int {
	err := os.Remove(path)
	if err == nil {
		return 0
	} else if os.IsNotExist(err) {
		return 1
	} else {
		return -1
	}
}

type entry struct {
	mtime    timestamp.TimeStamp
	statErr  error // if mtime is -1
	contents []byte
}

// MockDiskInterface provides a mock implementation for testing
type MockDiskInterface struct {
	directoriesMade map[string]struct{}
	filesRead       map[string]struct{}
	files           map[string]entry
	filesRemoved    map[string]struct{}
	filesCreated    map[string]struct{}
	now             timestamp.TimeStamp
}

// NewMockDiskInterface creates a new MockDiskInterface
func NewMockDiskInterface() *MockDiskInterface {
	return &MockDiskInterface{
		directoriesMade: make(map[string]struct{}, 0),
		filesRead:       make(map[string]struct{}, 0),
		files:           make(map[string]entry),
		filesRemoved:    make(map[string]struct{}, 0),
		filesCreated:    make(map[string]struct{}, 0),
		now:             timestamp.TimeStamp(1),
	}
}

// Stat returns the modification time of a mock file
func (m *MockDiskInterface) Stat(path string) (timestamp.TimeStamp, error) {
	if entry, ok := m.files[path]; ok {
		if entry.statErr != nil {
			return timestamp.TimeStampUnknown, entry.statErr
		}
		return entry.mtime, nil
	}
	return timestamp.TimeStampMissing, nil
}

// ReadFile reads the contents of a mock file
func (m *MockDiskInterface) ReadFile(path string) ([]byte, error) {
	m.filesRead[path] = struct{}{}
	if file, ok := m.files[path]; ok {
		return file.contents, nil
	}
	return nil, os.ErrNotExist
}

func (m *MockDiskInterface) Create(path string, contents []byte) error {
	return m.WriteFile(path, contents, false)
}

// WriteFile writes contents to a mock file
func (m *MockDiskInterface) WriteFile(path string, contents []byte, crlfOnWindows bool) error {
	m.files[path] = entry{
		contents: contents,
		mtime:    m.now,
	}
	m.filesCreated[path] = struct{}{}
	return nil
}

func (m *MockDiskInterface) MakeDir(path string) error {
	m.directoriesMade[path] = struct{}{}
	return nil
}

// MakeDir creates a mock directory
func (m *MockDiskInterface) MakeDirs(path string) error {
	for d := filepath.Dir(path); d != "." && d != "/"; d = filepath.Dir(d) {
		if err := m.MakeDir(d); err != nil {
			return err
		}
	}
	return nil
}

// RemoveFile removes a mock file
func (m *MockDiskInterface) RemoveFile(path string) int {
	if _, ok := m.directoriesMade[path]; ok {
		return -1
	}
	if _, ok := m.files[path]; !ok {
		return 1
	}
	delete(m.files, path)
	m.filesRemoved[path] = struct{}{}
	return 0
}

// AddFile adds a file to the mock file system
func (m *MockDiskInterface) AddFile(path string, contents []byte, mtime timestamp.TimeStamp) {
	m.files[path] = entry{
		contents: contents,
		mtime:    mtime,
	}
}

func (m *MockDiskInterface) FilesRead() []string {
	return slices.Sorted(maps.Keys(m.filesRead))
}

func (m *MockDiskInterface) DirectoriesMade() []string {
	return slices.Sorted(maps.Keys(m.directoriesMade))
}

func (m *MockDiskInterface) Tick() timestamp.TimeStamp {
	t2 := m.now + 1
	m.now = t2
	return t2
}

func (m *MockDiskInterface) Now() timestamp.TimeStamp {
	return m.now
}

func (m *MockDiskInterface) SetMtime(path string, mtime timestamp.TimeStamp) {
	val := m.files[path]
	val.mtime = mtime
	m.files[path] = val
}

func (m *MockDiskInterface) SetStatError(path string, err string) {
	m.files[path] = entry{
		mtime:   timestamp.TimeStampUnknown,
		statErr: fmt.Errorf("%s", err),
	}
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
