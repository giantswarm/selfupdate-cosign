package selfupdatecosign

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

// The release the fake GitHub lists: this platform's binary and its bundle.
const (
	binaryID int64 = 1
	bundleID int64 = 2
)

// onWindows: there Install is UpdateTo, and binaries end in .exe.
var onWindows = runtime.GOOS == "windows"

func binaryName() string {
	name := "tool-" + runtime.GOOS + "-" + runtime.GOARCH
	if onWindows {
		name += ".exe"
	}
	return name
}

type fakeSource struct{ assets map[int64][]byte }

func (s fakeSource) ListReleases(context.Context, selfupdate.Repository) ([]selfupdate.SourceRelease, error) {
	return []selfupdate.SourceRelease{fakeRelease{}}, nil
}

func (s fakeSource) DownloadReleaseAsset(_ context.Context, _ *selfupdate.Release, id int64) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.assets[id])), nil
}

type fakeRelease struct{}

func (fakeRelease) GetID() int64              { return 1 }
func (fakeRelease) GetTagName() string        { return "v2.0.0" }
func (fakeRelease) GetDraft() bool            { return false }
func (fakeRelease) GetPrerelease() bool       { return false }
func (fakeRelease) GetPublishedAt() time.Time { return time.Date(2026, 9, 24, 5, 0, 0, 0, time.UTC) }
func (fakeRelease) GetReleaseNotes() string   { return "" }
func (fakeRelease) GetName() string           { return "v2.0.0" }
func (fakeRelease) GetURL() string            { return "https://example.test/releases/v2.0.0" }
func (fakeRelease) GetAssets() []selfupdate.SourceAsset {
	return []selfupdate.SourceAsset{
		fakeAsset{bundleID, binaryName() + BundleSuffix},
		fakeAsset{binaryID, binaryName()},
	}
}

type fakeAsset struct {
	id   int64
	name string
}

func (a fakeAsset) GetID() int64                  { return a.id }
func (a fakeAsset) GetName() string               { return a.name }
func (a fakeAsset) GetSize() int                  { return 0 }
func (a fakeAsset) GetBrowserDownloadURL() string { return "https://example.test/download/" + a.name }

// accepting stands in for a Validator whose bundle verifies, refusing for one
// whose bundle does not: the check itself is validator_test.go's.
type accepting struct{}

func (accepting) GetValidationAssetName(name string) string { return name + BundleSuffix }
func (accepting) Validate(string, []byte, []byte) error     { return nil }

type refusing struct{}

func (refusing) GetValidationAssetName(name string) string { return name + BundleSuffix }
func (refusing) Validate(name string, _, _ []byte) error {
	return errors.New(name + " does not verify")
}

// latest is an updater over the fake GitHub, whose release carries binary,
// and that release as DetectLatest finds it.
func latest(t *testing.T, binary []byte, validator selfupdate.Validator) (*selfupdate.Updater, *selfupdate.Release) {
	t.Helper()
	src := fakeSource{assets: map[int64][]byte{binaryID: binary, bundleID: []byte("its bundle")}}
	up, err := selfupdate.NewUpdater(selfupdate.Config{Source: src, Validator: validator})
	if err != nil {
		t.Fatal(err)
	}
	rel, found, err := up.DetectLatest(context.Background(), selfupdate.ParseSlug("giantswarm/tool"))
	if err != nil || !found {
		t.Fatalf("DetectLatest = %v, %v", found, err)
	}
	return up, rel
}

func write(t *testing.T, path string, content []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode passes the umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s holds %d bytes %.20q…, want %d bytes %.20q…", path, len(got), got, len(want), want)
	}
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s holds %q, want %q", dir, got, want)
	}
}

func TestInstallReplacesTheBinaryInPlace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	write(t, exe, []byte("the installed tool"), 0o750)
	other := filepath.Join(dir, "tool-copy")
	write(t, other, []byte("another copy"), 0o755)
	link := filepath.Join(t.TempDir(), "tool")
	if err := os.Symlink(exe, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	up, rel := latest(t, []byte("the released tool"), accepting{})

	if err := Install(context.Background(), up, rel, link); err != nil {
		t.Fatalf("Install: %v", err)
	}

	assertContent(t, exe, []byte("the released tool"))
	if !onWindows {
		if info, err := os.Stat(exe); err != nil || info.Mode().Perm() != 0o750 {
			t.Errorf("mode after the update: %v %v, want the replaced binary's -rwxr-x---", info.Mode(), err)
		}
	}
	if dest, err := os.Readlink(link); err != nil || dest != exe {
		t.Errorf("the link names %q (%v), want %s", dest, err, exe)
	}
	assertContent(t, other, []byte("another copy"))
	assertEntries(t, dir, "tool", "tool-copy")
}

func TestInstallLeavesTheBinaryAloneWhenTheDownloadDoesNotVerify(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	write(t, exe, []byte("the installed tool"), 0o755)
	up, rel := latest(t, []byte("a tampered tool"), refusing{})

	err := Install(context.Background(), up, rel, exe)
	if err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("Install = %v, want the validator's refusal", err)
	}
	assertContent(t, exe, []byte("the installed tool"))
	assertEntries(t, dir, "tool")
}

// Several updates of one binary at once, as when every shell on a machine
// runs `<tool> version update` after a release: the binary is complete at
// every moment, and nothing else is left in its directory. go-selfupdate's own
// swap fails this test: the watcher finds no binary between its two renames,
// and three updates at once regularly end with none at all.
func TestInstallKeepsTheBinaryWholeWhileUpdatesRunAtOnce(t *testing.T) {
	if onWindows {
		t.Skip("on Windows Install is UpdateTo")
	}
	installed := bytes.Repeat([]byte("o"), 4<<20)
	released := bytes.Repeat([]byte("n"), 8<<20)
	up, rel := latest(t, released, accepting{})
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")

	for round := range 10 {
		write(t, exe, installed, 0o755)

		var stop atomic.Bool
		seen := make(chan string, 1)
		go func() {
			defer close(seen)
			for !stop.Load() {
				info, err := os.Stat(exe)
				switch {
				case err != nil:
					seen <- err.Error()
					return
				case info.Size() != int64(len(installed)) && info.Size() != int64(len(released)):
					seen <- fmt.Sprintf("a file of %d bytes", info.Size())
					return
				}
			}
		}()

		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Go(func() { errs[i] = Install(context.Background(), up, rel, exe) })
		}
		wg.Wait()
		stop.Store(true)

		if err := errors.Join(errs...); err != nil {
			t.Fatalf("round %d: Install: %v", round, err)
		}
		if s, ok := <-seen; ok {
			t.Fatalf("round %d: while the updates ran, the watcher found %s", round, s)
		}
		assertContent(t, exe, released)
		assertEntries(t, dir, "tool")
	}
}
