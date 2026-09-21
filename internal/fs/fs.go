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

// Package fs implements filterfs: a FUSE passthrough view of a source
// directory with selected paths hidden.
package fs

import (
	"fmt"
	"syscall"
	"time"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// Options configures a filterfs mount.
type Options struct {
	Source     string   // absolute path of the directory to expose
	Mountpoint string   // absolute path to mount on
	Excludes   []string // exact paths, relative to the mount root, to hide
	AllowOther bool     // let users other than the mounter access the mount
	Debug      bool     // log every FUSE request
}

// NewRoot returns the root node for a filtered view of source.
func NewRoot(source string, filter *Filter) (fusefs.InodeEmbedder, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(source, &st); err != nil {
		return nil, err
	}
	rootData := &fusefs.LoopbackRoot{Path: source, Dev: uint64(st.Dev)}
	root := &node{LoopbackNode: &fusefs.LoopbackNode{RootData: rootData}, filter: filter}
	rootData.RootNode = root
	return root, nil
}

// Mount mounts filterfs and returns once the mount is ready. Callers must
// eventually call Unmount and/or Wait on the returned server.
func Mount(o Options) (*fuse.Server, error) {
	filter, err := NewFilter(o.Excludes)
	if err != nil {
		return nil, err
	}
	root, err := NewRoot(o.Source, filter)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", o.Source, err)
	}

	// The view must be live: changes made directly in the source tree have
	// to show up immediately, so the kernel must not cache entries,
	// attributes or negative lookups.
	var zero time.Duration
	opts := &fusefs.Options{
		EntryTimeout:    &zero,
		AttrTimeout:     &zero,
		NegativeTimeout: &zero,
		MountOptions: fuse.MountOptions{
			FsName:      o.Source,
			Name:        "filterfs",
			AllowOther:  o.AllowOther,
			Debug:       o.Debug,
			DirectMount: syscall.Geteuid() == 0,
			// Let the kernel enforce permission bits, as for a local fs.
			Options: []string{"default_permissions"},
		},
	}
	return fusefs.Mount(o.Mountpoint, root, opts)
}
