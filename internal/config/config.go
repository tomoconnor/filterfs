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

// Package config parses and validates filterfs command lines, both the
// native "filterfs --source ... MOUNTPOINT" form and the mount(8) helper
// form "mount.filterfs SOURCE MOUNTPOINT -o exclude=...".
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	filterfs "github.com/tomoconnor/filterfs/internal/fs"
)

// Config is a fully parsed filterfs invocation.
type Config struct {
	Source     string
	Mountpoint string
	Excludes   []string
	Debug      bool
	Foreground bool
	AllowOther bool
}

// ErrHelp is returned when the user asked for usage information.
var ErrHelp = flag.ErrHelp

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// Usage is the help text for the native command line.
const Usage = `usage: filterfs --source DIR [--exclude PATH]... [options] MOUNTPOINT

Expose DIR at MOUNTPOINT as a live read/write passthrough view, with each
excluded PATH (relative to the mount root, e.g. /.git) hidden.

options:
  --source DIR      directory to expose (required)
  --exclude PATH    path to hide, e.g. /.git; may be repeated
  --foreground, -f  stay in the foreground instead of daemonizing
  --debug, -d       log every FUSE request (implies --foreground)
  --allow-other     allow other users to access the mount
                    (needs user_allow_other in /etc/fuse.conf unless root)

unmount with: fusermount3 -u MOUNTPOINT  (or: umount MOUNTPOINT)
`

// Parse parses the native command line (without the program name).
func Parse(args []string) (*Config, error) {
	var c Config
	var excludes stringList
	fset := flag.NewFlagSet("filterfs", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	fset.StringVar(&c.Source, "source", "", "")
	fset.Var(&excludes, "exclude", "")
	fset.BoolVar(&c.Foreground, "foreground", false, "")
	fset.BoolVar(&c.Foreground, "f", false, "")
	fset.BoolVar(&c.Debug, "debug", false, "")
	fset.BoolVar(&c.Debug, "d", false, "")
	fset.BoolVar(&c.AllowOther, "allow-other", false, "")

	// The flag package stops at the first positional argument; keep going
	// so flags may appear after the mountpoint too.
	var positional []string
	for {
		if err := fset.Parse(args); err != nil {
			return nil, err
		}
		args = fset.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}

	if c.Source == "" {
		return nil, errors.New("--source is required")
	}
	if len(positional) != 1 {
		return nil, fmt.Errorf("expected exactly one MOUNTPOINT argument, got %d", len(positional))
	}
	c.Mountpoint = positional[0]
	c.Excludes = excludes
	if c.Debug {
		c.Foreground = true
	}
	return &c, nil
}

// MountUsage is the help text for the mount(8) helper form.
const MountUsage = `usage: mount.filterfs SOURCE MOUNTPOINT [-o exclude=PATH[,exclude=PATH...]]

mount options:
  exclude=PATH   hide PATH (relative to the mount root); may be repeated
  allow_other    allow other users to access the mount
  debug          log every FUSE request and stay in the foreground
  foreground     stay in the foreground

Standard options such as rw, defaults, noauto, user, nofail and _netdev are
accepted and ignored. Exclude paths cannot contain commas.
`

// ignoredMountOptions are generic mount(8)/fstab options that make no
// difference to filterfs.
var ignoredMountOptions = map[string]bool{
	"rw": true, "defaults": true, "auto": true, "noauto": true,
	"user": true, "nouser": true, "users": true, "owner": true,
	"nofail": true, "_netdev": true, "dev": true, "nodev": true,
	"suid": true, "nosuid": true, "exec": true, "noexec": true,
	"atime": true, "noatime": true, "relatime": true, "async": true,
}

// ParseMount parses a mount(8) helper command line (without the program
// name): "SOURCE MOUNTPOINT [-o OPTS] [-f] [-n] [-s] [-v]".
func ParseMount(args []string) (*Config, error) {
	var c Config
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o":
			if i+1 >= len(args) {
				return nil, errors.New("-o requires an argument")
			}
			i++
			if err := c.applyMountOptions(args[i]); err != nil {
				return nil, err
			}
		case strings.HasPrefix(a, "-o"):
			if err := c.applyMountOptions(a[2:]); err != nil {
				return nil, err
			}
		case a == "-h" || a == "--help":
			return nil, ErrHelp
		case a == "-f":
			c.Foreground = true
		case a == "-n" || a == "-s" || a == "-v":
			// no mtab / sloppy / verbose: nothing to do
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown option %q", a)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return nil, fmt.Errorf("expected SOURCE and MOUNTPOINT, got %d arguments", len(positional))
	}
	c.Source, c.Mountpoint = positional[0], positional[1]
	if c.Debug {
		c.Foreground = true
	}
	return &c, nil
}

func (c *Config) applyMountOptions(opts string) error {
	for _, o := range strings.Split(opts, ",") {
		key, val, hasVal := strings.Cut(o, "=")
		switch {
		case o == "":
		case key == "exclude" && hasVal:
			c.Excludes = append(c.Excludes, val)
		case o == "allow_other":
			c.AllowOther = true
		case o == "debug":
			c.Debug = true
		case o == "foreground":
			c.Foreground = true
		case ignoredMountOptions[o]:
		default:
			return fmt.Errorf("unsupported mount option %q", o)
		}
	}
	return nil
}

// Validate resolves Source and Mountpoint to absolute, symlink-free paths,
// checks the exclusions, and refuses configurations where the mount would
// contain itself or hide its own source.
func (c *Config) Validate() error {
	filter, err := filterfs.NewFilter(c.Excludes)
	if err != nil {
		return err
	}
	src, err := resolveDir(c.Source)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	mnt, err := resolveDir(c.Mountpoint)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}

	if src == mnt {
		return fmt.Errorf("mountpoint %s is the same directory as the source", mnt)
	}
	if rel, ok := within(src, mnt); ok && !filter.Excluded(rel) {
		return fmt.Errorf("mountpoint %s is inside the source tree and would expose itself recursively; "+
			"use a mountpoint outside %s, or exclude /%s", mnt, src, rel)
	}
	if _, ok := within(mnt, src); ok {
		return fmt.Errorf("source %s is inside the mountpoint %s; mounting would hide the source from filterfs itself", src, mnt)
	}

	c.Source, c.Mountpoint = src, mnt
	return nil
}

// resolveDir returns the absolute, symlink-resolved form of p and checks
// that it is a directory.
func resolveDir(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", real)
	}
	return real, nil
}

// within reports whether path p is strictly inside dir, returning p
// relative to dir. Both must be clean absolute paths.
func within(dir, p string) (string, bool) {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}
