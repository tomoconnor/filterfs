// Copyright (c) 2026 Tom O'Connor <tom@twinhelix.org>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

//go:build linux

package fs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mountTest creates a source tree, mounts it with the given excludes, and
// returns the source and mount directories. The test is skipped if FUSE is
// not usable in this environment.
func mountTest(t *testing.T, excludes ...string) (src, mnt string) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("FUSE not available: /dev/fuse missing")
	}
	tmp := t.TempDir()
	src = filepath.Join(tmp, "src")
	mnt = filepath.Join(tmp, "mnt")

	for _, d := range []string{".git/refs", "src/sub", "foo/private/deep", "foo/public", "nested/.git", "worktree"} {
		must(t, os.MkdirAll(filepath.Join(src, d), 0o755))
	}
	for name, data := range map[string]string{
		".git/config":          "[core]\n",
		".env":                 "SECRET=1\n",
		"hello.txt":            "hello\n",
		"src/main.go":          "package main\n",
		"foo/private/deep/key": "key\n",
		"foo/public/readme":    "public\n",
		"nested/.git/config":   "nested\n",
		"worktree/.git":        "gitdir: /elsewhere\n",
		".gitignore":           "*.o\n",
	} {
		must(t, os.WriteFile(filepath.Join(src, name), []byte(data), 0o644))
	}
	must(t, os.Mkdir(mnt, 0o755))

	server, err := Mount(Options{Source: src, Mountpoint: mnt, Excludes: excludes})
	if err != nil {
		t.Skipf("FUSE mount not permitted here: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		server.Wait()
	})
	return src, mnt
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	must(t, err)
	return string(b)
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	must(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func wantErrno(t *testing.T, what string, err error, want syscall.Errno) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: got error %v, want %v", what, err, want)
	}
}

func TestExcludedPathsAreInvisible(t *testing.T) {
	_, mnt := mountTest(t, "/.git", "/.env", "/foo/private", "/worktree/.git")

	root := names(t, mnt)
	for _, hidden := range []string{".git", ".env"} {
		if slices.Contains(root, hidden) {
			t.Errorf("readdir(/) lists %s: %v", hidden, root)
		}
	}
	for _, visible := range []string{"hello.txt", "src", "foo", ".gitignore", "nested", "worktree"} {
		if !slices.Contains(root, visible) {
			t.Errorf("readdir(/) is missing %s: %v", visible, root)
		}
	}
	if got := names(t, filepath.Join(mnt, "foo")); !slices.Equal(got, []string{"public"}) {
		t.Errorf("readdir(/foo) = %v, want [public]", got)
	}
	if got := names(t, filepath.Join(mnt, "worktree")); len(got) != 0 {
		t.Errorf("readdir(/worktree) = %v, want [] (.git file hidden)", got)
	}
	if got := names(t, filepath.Join(mnt, "nested")); !slices.Equal(got, []string{".git"}) {
		t.Errorf("readdir(/nested) = %v, want [.git] (only /.git is excluded)", got)
	}

	for _, p := range []string{
		".git", ".git/config", ".git/refs", ".git/nonexistent",
		".env",
		"foo/private", "foo/private/deep", "foo/private/deep/key",
		"worktree/.git",
		// Path normalization is done by the kernel, but check anyway.
		"src/../.git", "src/../.git/config", "./.git", "foo/public/../private",
	} {
		full := mnt + "/" + p
		_, err := os.Lstat(full)
		wantErrno(t, "lstat "+p, err, syscall.ENOENT)
		_, err = os.Open(full)
		wantErrno(t, "open "+p, err, syscall.ENOENT)
	}
	_, err := os.Lstat(mnt + "//.git")
	wantErrno(t, "lstat //.git", err, syscall.ENOENT)

	if got := readFile(t, filepath.Join(mnt, "nested/.git/config")); got != "nested\n" {
		t.Errorf("nested/.git/config = %q", got)
	}
	if got := readFile(t, filepath.Join(mnt, "foo/public/readme")); got != "public\n" {
		t.Errorf("foo/public/readme = %q", got)
	}
}

func TestExcludedSymlinkAndFile(t *testing.T) {
	src, mnt := mountTest(t, "/link", "/hello.txt")
	must(t, os.Symlink("src", filepath.Join(src, "link")))

	root := names(t, mnt)
	if slices.Contains(root, "link") || slices.Contains(root, "hello.txt") {
		t.Errorf("readdir(/) = %v, want no link or hello.txt", root)
	}
	_, err := os.Lstat(filepath.Join(mnt, "link"))
	wantErrno(t, "lstat link", err, syscall.ENOENT)
	_, err = os.Readlink(filepath.Join(mnt, "link"))
	wantErrno(t, "readlink link", err, syscall.ENOENT)
	_, err = os.Stat(filepath.Join(mnt, "link/main.go"))
	wantErrno(t, "stat link/main.go", err, syscall.ENOENT)
	_, err = os.Stat(filepath.Join(mnt, "hello.txt"))
	wantErrno(t, "stat hello.txt", err, syscall.ENOENT)
}

func TestSymlinksCannotRevealExcludedPaths(t *testing.T) {
	src, mnt := mountTest(t, "/.git")
	must(t, os.Symlink(".git", filepath.Join(src, "gitlink")))
	must(t, os.Symlink("../.git/config", filepath.Join(src, "src/cfglink")))
	must(t, os.Symlink("hello.txt", filepath.Join(src, "hellolink")))

	// Symlink contents are passed through unchanged...
	target, err := os.Readlink(filepath.Join(mnt, "gitlink"))
	must(t, err)
	if target != ".git" {
		t.Errorf("readlink gitlink = %q", target)
	}
	// ...but are resolved by the kernel inside the mount, so they end up
	// at the excluded path, which does not exist.
	_, err = os.Stat(filepath.Join(mnt, "gitlink"))
	wantErrno(t, "stat gitlink", err, syscall.ENOENT)
	_, err = os.ReadFile(filepath.Join(mnt, "gitlink/config"))
	wantErrno(t, "read gitlink/config", err, syscall.ENOENT)
	_, err = os.ReadFile(filepath.Join(mnt, "src/cfglink"))
	wantErrno(t, "read src/cfglink", err, syscall.ENOENT)

	if got := readFile(t, filepath.Join(mnt, "hellolink")); got != "hello\n" {
		t.Errorf("read hellolink = %q", got)
	}
}

func TestCannotCreateOrModifyExcludedPaths(t *testing.T) {
	src, mnt := mountTest(t, "/.git", "/.env", "/foo/private", "/newdir")
	p := func(s string) string { return filepath.Join(mnt, s) }

	// Operations inside an excluded directory: its parent does not exist.
	_, err := os.Create(p(".git/newfile"))
	wantErrno(t, "create .git/newfile", err, syscall.ENOENT)
	wantErrno(t, "mkdir .git/newdir", os.Mkdir(p(".git/newdir"), 0o755), syscall.ENOENT)
	wantErrno(t, "write .git/config", os.WriteFile(p(".git/config"), []byte("x"), 0o644), syscall.ENOENT)
	wantErrno(t, "remove .git/config", os.Remove(p(".git/config")), syscall.ENOENT)
	wantErrno(t, "symlink .git/l", os.Symlink("x", p(".git/l")), syscall.ENOENT)
	wantErrno(t, "link .git/h", os.Link(p("hello.txt"), p(".git/h")), syscall.ENOENT)
	wantErrno(t, "rename into .git", os.Rename(p("hello.txt"), p(".git/hello.txt")), syscall.ENOENT)
	wantErrno(t, "rename out of .git", os.Rename(p(".git/config"), p("config")), syscall.ENOENT)
	wantErrno(t, "mkdir foo/private/x", os.Mkdir(p("foo/private/x"), 0o755), syscall.ENOENT)
	wantErrno(t, "chmod .git", os.Chmod(p(".git"), 0o700), syscall.ENOENT)

	// Removing an excluded entry: it does not exist.
	wantErrno(t, "unlink .env", os.Remove(p(".env")), syscall.ENOENT)
	wantErrno(t, "rmdir .git", syscall.Rmdir(p(".git")), syscall.ENOENT)
	wantErrno(t, "rename .env", os.Rename(p(".env"), p("env")), syscall.ENOENT)

	// Creating something at an excluded path is refused.
	wantErrno(t, "mkdir .git", os.Mkdir(p(".git"), 0o755), syscall.EPERM)
	wantErrno(t, "mkdir newdir", os.Mkdir(p("newdir"), 0o755), syscall.EPERM)
	_, err = os.OpenFile(p(".env"), os.O_CREATE|os.O_WRONLY, 0o644)
	wantErrno(t, "create .env", err, syscall.EPERM)
	wantErrno(t, "symlink .env", os.Symlink("x", p(".env")), syscall.EPERM)
	wantErrno(t, "link .env", os.Link(p("hello.txt"), p(".env")), syscall.EPERM)
	wantErrno(t, "rename onto .env", os.Rename(p("hello.txt"), p(".env")), syscall.EPERM)
	wantErrno(t, "rename dir onto foo/private", os.Rename(p("src"), p("foo/private")), syscall.EPERM)
	wantErrno(t, "mknod .env", syscall.Mknod(p(".env"), syscall.S_IFIFO|0o644, 0), syscall.EPERM)

	// The source is untouched.
	if got := readFile(t, filepath.Join(src, ".env")); got != "SECRET=1\n" {
		t.Errorf("source .env = %q", got)
	}
	if got := readFile(t, filepath.Join(src, ".git/config")); got != "[core]\n" {
		t.Errorf("source .git/config = %q", got)
	}
	if got := readFile(t, filepath.Join(src, "hello.txt")); got != "hello\n" {
		t.Errorf("source hello.txt = %q", got)
	}
	for _, name := range []string{"newdir", ".git/newfile", ".git/newdir", "config", "env"} {
		if _, err := os.Lstat(filepath.Join(src, name)); err == nil {
			t.Errorf("source %s was created", name)
		}
	}
}

func TestPassthroughBothWays(t *testing.T) {
	src, mnt := mountTest(t, "/.git")

	// Mount -> source.
	must(t, os.WriteFile(filepath.Join(mnt, "hello.txt"), []byte("changed\n"), 0o644))
	if got := readFile(t, filepath.Join(src, "hello.txt")); got != "changed\n" {
		t.Errorf("source after write via mount = %q", got)
	}

	// Source -> mount, including a size change visible without reopening.
	must(t, os.WriteFile(filepath.Join(src, "hello.txt"), []byte("source-change, longer\n"), 0o644))
	if got := readFile(t, filepath.Join(mnt, "hello.txt")); got != "source-change, longer\n" {
		t.Errorf("mount after write via source = %q", got)
	}
	fi, err := os.Stat(filepath.Join(mnt, "hello.txt"))
	must(t, err)
	if fi.Size() != int64(len("source-change, longer\n")) {
		t.Errorf("size via mount = %d", fi.Size())
	}

	// New and deleted files in the source show up immediately.
	must(t, os.WriteFile(filepath.Join(src, "new.txt"), []byte("new\n"), 0o644))
	if got := readFile(t, filepath.Join(mnt, "new.txt")); got != "new\n" {
		t.Errorf("new file via mount = %q", got)
	}
	must(t, os.Remove(filepath.Join(src, "new.txt")))
	_, err = os.Stat(filepath.Join(mnt, "new.txt"))
	wantErrno(t, "stat removed file", err, syscall.ENOENT)

	// Appends through the mount.
	f, err := os.OpenFile(filepath.Join(mnt, "hello.txt"), os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString("appended\n")
	must(t, err)
	must(t, f.Close())
	if got := readFile(t, filepath.Join(src, "hello.txt")); got != "source-change, longer\nappended\n" {
		t.Errorf("source after append = %q", got)
	}

	// An excluded path that appears in the source later is still hidden.
	must(t, os.RemoveAll(filepath.Join(src, ".git")))
	must(t, os.Mkdir(filepath.Join(src, ".git"), 0o755))
	if _, err := os.Stat(filepath.Join(mnt, ".git")); !errors.Is(err, syscall.ENOENT) {
		t.Errorf("recreated .git visible: %v", err)
	}
}

func TestNormalOperations(t *testing.T) {
	src, mnt := mountTest(t, "/.git")
	m := func(s string) string { return filepath.Join(mnt, s) }
	s := func(s string) string { return filepath.Join(src, s) }

	must(t, os.MkdirAll(m("a/b"), 0o750))
	if fi, err := os.Stat(s("a/b")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750&^currentUmask() {
		t.Errorf("mkdir via mount: %v %v", fi, err)
	}

	must(t, os.WriteFile(m("a/b/f"), []byte("data"), 0o600))
	must(t, os.Rename(m("a/b/f"), m("a/g")))
	if got := readFile(t, s("a/g")); got != "data" {
		t.Errorf("renamed file = %q", got)
	}

	must(t, os.Chmod(m("a/g"), 0o640))
	if fi, _ := os.Stat(s("a/g")); fi.Mode().Perm() != 0o640 {
		t.Errorf("chmod: source mode %v", fi.Mode())
	}

	mtime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	must(t, os.Chtimes(m("a/g"), mtime, mtime))
	if fi, _ := os.Stat(s("a/g")); !fi.ModTime().Equal(mtime) {
		t.Errorf("chtimes: source mtime %v", fi.ModTime())
	}

	must(t, os.Truncate(m("a/g"), 2))
	if got := readFile(t, s("a/g")); got != "da" {
		t.Errorf("truncate: %q", got)
	}

	must(t, os.Symlink("g", m("a/sym")))
	if target, err := os.Readlink(s("a/sym")); err != nil || target != "g" {
		t.Errorf("symlink: %q %v", target, err)
	}

	must(t, os.Link(m("a/g"), m("a/hard")))
	var st1, st2 syscall.Stat_t
	must(t, syscall.Stat(s("a/g"), &st1))
	must(t, syscall.Stat(s("a/hard"), &st2))
	if st1.Ino != st2.Ino || st1.Nlink != 2 {
		t.Errorf("hardlink: ino %d/%d nlink %d", st1.Ino, st2.Ino, st1.Nlink)
	}

	var mst syscall.Stat_t
	must(t, syscall.Stat(m("a/g"), &mst))
	if mst.Uid != st1.Uid || mst.Gid != st1.Gid || mst.Size != st1.Size || mst.Mode != st1.Mode {
		t.Errorf("stat via mount %+v differs from source %+v", mst, st1)
	}

	must(t, os.Remove(m("a/hard")))
	must(t, os.Remove(m("a/sym")))
	must(t, os.Remove(m("a/g")))
	must(t, os.Remove(m("a/b")))
	must(t, os.Remove(m("a")))
	if _, err := os.Lstat(s("a")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("source a still exists: %v", err)
	}

	// Large-ish directory listing, to exercise multiple READDIR batches
	// with filtered entries in between.
	must(t, os.Mkdir(s("many"), 0o755))
	for i := 0; i < 500; i++ {
		must(t, os.WriteFile(filepath.Join(s("many"), strings.Repeat("x", i%50+1)+"-"+strconv.Itoa(i)), nil, 0o644))
	}
	if got := len(names(t, m("many"))); got != 500 {
		t.Errorf("readdir(many) returned %d entries, want 500", got)
	}
}

func currentUmask() os.FileMode {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return os.FileMode(old)
}
