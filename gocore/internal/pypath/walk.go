package pypath

import (
	"io/fs"
	"os"
)

// Walk is os.walk(top, topdown=True, onerror=onError, followlinks=False) as
// implemented in Python 3.12+: a depth-first, pre-order traversal that lists
// each directory in the operating system's order (like os.scandir, not
// sorted), classifies entries with is_dir() (which follows symlinks), and
// does not descend into symlinked directories.
//
// visit receives the directory path, its subdirectory names and its other
// entry names. It may reorder or shrink *dirs to prune the walk, and returns
// false to stop walking (Python's break out of the loop). onError (may be
// nil) receives directory listing errors; that directory is then skipped.
func Walk(top string, onError func(error), visit func(dir string, dirs *[]string, files []string) bool) {
	stack := []string{top}
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := readDirUnsorted(dir)
		if err != nil {
			if onError != nil {
				onError(err)
			}
			continue
		}
		var dirs, files []string
		for _, entry := range entries {
			isDir := entry.IsDir()
			if entry.Type()&fs.ModeSymlink != 0 {
				info, statErr := os.Stat(Join(dir, entry.Name()))
				isDir = statErr == nil && info.IsDir()
			}
			if isDir {
				dirs = append(dirs, entry.Name())
			} else {
				files = append(files, entry.Name())
			}
		}
		if !visit(dir, &dirs, files) {
			return
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			next := Join(dir, dirs[i])
			if info, err := os.Lstat(next); err == nil && info.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			stack = append(stack, next)
		}
	}
}

// readDirUnsorted lists a directory in the order the operating system
// returns it (os.ReadDir would sort by name). A listing error part way
// through is reported like os.scandir's iteration error.
func readDirUnsorted(dir string) ([]fs.DirEntry, error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}
