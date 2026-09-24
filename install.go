package selfupdatecosign

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/creativeprojects/go-selfupdate"
)

// Install writes release over the executable at path the way
// updater.UpdateTo(ctx, release, path) does, the download checked by the
// updater's validator first, but with a single rename: every process that
// starts path meanwhile runs the old binary or the new one, never a missing or
// a half-written file, and any number of Install calls may run at once.
//
// UpdateTo swaps through two fixed names next to path: it writes
// .<name>.new, renames path to .<name>.old, renames .<name>.new to path and
// removes .<name>.old. Between the two renames there is no binary at path,
// and two updates that run at once share those names: one truncates the
// other's .<name>.new or removes the binary the other just moved in, and path
// can end up missing. Install lets UpdateTo swap a placeholder in a directory
// of its own next to path instead, then renames the result over path: the
// rename replaces the old binary atomically, it keeps no copy of it, and the
// directory is all it removes.
//
// path is resolved first, so a symbolic link keeps pointing at the binary it
// named. The installed binary keeps the mode of the one it replaces. On
// Windows, where a running executable can be moved aside but not replaced,
// Install is UpdateTo.
func Install(ctx context.Context, updater *selfupdate.Updater, release *selfupdate.Release, path string) error {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return updater.UpdateTo(ctx, release, target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()

	dir, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".update-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// UpdateTo replaces a file that exists, and names the executable it looks
	// for in an archive after it.
	staged := filepath.Join(dir, filepath.Base(target))
	if err := os.WriteFile(staged, nil, 0o600); err != nil {
		return err
	}
	if err := updater.UpdateTo(ctx, release, staged); err != nil {
		return err
	}
	if err := os.Chmod(staged, mode); err != nil {
		return err
	}
	return os.Rename(staged, target)
}
