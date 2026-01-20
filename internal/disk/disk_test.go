package disk_test

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/buildbuddy-io/reninja/internal/disk"
	"github.com/buildbuddy-io/reninja/internal/timestamp"
	"github.com/stretchr/testify/require"
)

func TestStatMissingFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	diskInterface := disk.NewRealDiskInterface()

	mtime, err := diskInterface.Stat("nosuchfile")
	require.NoError(t, err)
	require.Equal(t, timestamp.TimeStampMissing, mtime)
}

func TestStatBatPath(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	diskInterface := disk.NewRealDiskInterface()
	var badPath string
	if runtime.GOOS == "windows" {
		badPath = "cc:\\foo"
	} else {
		badPath = strings.Repeat("x", 512)
	}
	mtime, err := diskInterface.Stat(badPath)
	require.Error(t, err)
	require.Equal(t, timestamp.TimeStampUnknown, mtime)
}

func TestStatExistingFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	err = os.WriteFile("file", []byte{}, 0644)
	require.NoError(t, err)

	mtime, err := diskInterface.Stat("file")
	require.NoError(t, err)
	require.Greater(t, mtime, timestamp.TimeStamp(1))
}

func TestStatExistingDir(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	err = diskInterface.MakeDir("subdir")
	require.NoError(t, err)
	err = diskInterface.MakeDir("subdir/subsubdir")
	require.NoError(t, err)

	parentMtime, err := diskInterface.Stat("..")
	require.NoError(t, err)
	require.Greater(t, parentMtime, timestamp.TimeStamp(1))

	currentMtime, err := diskInterface.Stat(".")
	require.NoError(t, err)
	require.Greater(t, currentMtime, timestamp.TimeStamp(1))

	subdirMtime, err := diskInterface.Stat("subdir")
	require.NoError(t, err)
	require.Greater(t, subdirMtime, timestamp.TimeStamp(1))

	subsubdirMtime, err := diskInterface.Stat("subdir/subsubdir")
	require.NoError(t, err)
	require.Greater(t, subsubdirMtime, timestamp.TimeStamp(1))

	subdirDotMtime, err := diskInterface.Stat("subdir/.")
	require.NoError(t, err)
	require.Equal(t, subdirMtime, subdirDotMtime)

	subdirParentMtime, err := diskInterface.Stat("subdir/subsubdir/..")
	require.NoError(t, err)
	require.Equal(t, subdirMtime, subdirParentMtime)

	subsubdirDotMtime, err := diskInterface.Stat("subdir/subsubdir/.")
	require.NoError(t, err)
	require.Equal(t, subsubdirMtime, subsubdirDotMtime)
}

func TestReadFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	content, err := diskInterface.ReadFile("foobar")
	require.Error(t, err)
	require.Equal(t, []byte(nil), content)
	require.True(t, os.IsNotExist(err))

	testContent := "test content\nok"
	err = os.WriteFile("testfile", []byte(testContent), 0644)
	require.NoError(t, err)

	content, err = diskInterface.ReadFile("testfile")
	require.NoError(t, err)
	require.Equal(t, []byte(testContent), content)
}

func TestMakeDirs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	path := "path/with/double//slash/"
	err = diskInterface.MakeDirs(path)
	require.NoError(t, err)

	err = os.WriteFile(path+"a_file", []byte{}, 0644)
	require.NoError(t, err)

	if runtime.GOOS == "windows" {
		path2 := "another\\with\\back\\\\slashes\\"
		err = diskInterface.MakeDirs(path2)
		require.NoError(t, err)

		err = os.WriteFile(path2+"a_file", []byte{}, 0644)
		require.NoError(t, err)
	}
}

func TestRemoveFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	fileName := "file-to-remove"
	err = os.WriteFile(fileName, []byte{}, 0644)
	require.NoError(t, err)

	result := diskInterface.RemoveFile(fileName)
	require.Equal(t, 0, result)

	result = diskInterface.RemoveFile(fileName)
	require.Equal(t, 1, result)

	result = diskInterface.RemoveFile("does not exist")
	require.Equal(t, 1, result)

	if runtime.GOOS == "windows" {
		err = os.WriteFile(fileName, []byte{}, 0644)
		require.NoError(t, err)

		err = os.Chmod(fileName, 0444)
		require.NoError(t, err)

		result = diskInterface.RemoveFile(fileName)
		require.Equal(t, 0, result)

		result = diskInterface.RemoveFile(fileName)
		require.Equal(t, 1, result)
	}
}

func TestRemoveDirectory(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()

	directoryName := "directory-to-remove"
	err = diskInterface.MakeDir(directoryName)
	require.NoError(t, err)

	result := diskInterface.RemoveFile(directoryName)
	require.Equal(t, 0, result)

	result = diskInterface.RemoveFile(directoryName)
	require.Equal(t, 1, result)

	result = diskInterface.RemoveFile("does not exist")
	require.Equal(t, 1, result)
}

func TestStatSymlink(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "Ninja-DiskInterfaceTest")
	require.NoError(t, err)
	t.Cleanup(func() {
		os.RemoveAll(tempDir)
	})

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	diskInterface := disk.NewRealDiskInterface()
	err = os.WriteFile("file", []byte("content"), 0644)
	require.NoError(t, err)

	fileMtime, err := diskInterface.Stat("file")
	require.NoError(t, err)
	require.Greater(t, fileMtime, timestamp.TimeStamp(1))

	// Create a symlink to the file.
	err = os.Symlink("file", "symlink")
	require.NoError(t, err)

	// Assert that stating the symlink will resolve the timestamp for the
	// linked file.
	symlinkMtime, err := diskInterface.Stat("symlink")
	require.NoError(t, err)
	require.Equal(t, fileMtime, symlinkMtime)
}
