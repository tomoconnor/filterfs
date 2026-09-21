package fs

import (
	"context"
	"syscall"

	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// node is a go-fuse LoopbackNode that hides excluded paths.
//
// All real work (I/O, attributes, xattrs, links, ...) is delegated to the
// embedded LoopbackNode, which operates directly on the source tree. This
// type only intercepts the operations that take a child name, because
// those are the only ways to reach a path the kernel does not already hold
// an inode for:
//
//   - Lookup and directory listings, so excluded entries never get an
//     inode and are never listed. Every other operation on an existing
//     node (open, getattr, setattr, read, write, ...) therefore only ever
//     sees non-excluded nodes.
//   - Create/Mkdir/Mknod/Symlink/Link/Rename-to, so nothing can be created
//     at, or moved onto, an excluded path.
//   - Unlink/Rmdir/Rename-from, so excluded entries cannot be removed or
//     moved away through the mount.
type node struct {
	*fusefs.LoopbackNode
	filter *Filter
}

var (
	_ fusefs.NodeWrapChilder    = (*node)(nil)
	_ fusefs.NodeLookuper       = (*node)(nil)
	_ fusefs.NodeOpendirHandler = (*node)(nil)
	_ fusefs.NodeReaddirer      = (*node)(nil)
	_ fusefs.NodeCreater        = (*node)(nil)
	_ fusefs.NodeMkdirer        = (*node)(nil)
	_ fusefs.NodeMknoder        = (*node)(nil)
	_ fusefs.NodeSymlinker      = (*node)(nil)
	_ fusefs.NodeLinker         = (*node)(nil)
	_ fusefs.NodeUnlinker       = (*node)(nil)
	_ fusefs.NodeRmdirer        = (*node)(nil)
	_ fusefs.NodeRenamer        = (*node)(nil)
)

// WrapChild is called by go-fuse for every inode the LoopbackNode creates
// below this one, so the whole tree consists of filtering nodes.
func (n *node) WrapChild(ctx context.Context, ops fusefs.InodeEmbedder) fusefs.InodeEmbedder {
	return &node{LoopbackNode: ops.(*fusefs.LoopbackNode), filter: n.filter}
}

// relPath is this node's path relative to the mount root, as tracked by
// go-fuse's own inode tree (i.e. the path the kernel used to reach it).
func (n *node) relPath() string {
	return n.Path(n.Root())
}

func (n *node) hidden(name string) bool {
	return n.filter.ExcludedChild(n.relPath(), name)
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if n.hidden(name) {
		return nil, syscall.ENOENT
	}
	return n.LoopbackNode.Lookup(ctx, name, out)
}

func (n *node) OpendirHandle(ctx context.Context, flags uint32) (fusefs.FileHandle, uint32, syscall.Errno) {
	fh, fuseFlags, errno := n.LoopbackNode.OpendirHandle(ctx, flags)
	if errno != 0 {
		return nil, 0, errno
	}
	return &dirHandle{inner: fh, dir: n.relPath(), filter: n.filter}, fuseFlags, 0
}

// Readdir is not used by go-fuse while OpendirHandle is implemented, but
// it is overridden so the unfiltered LoopbackNode.Readdir is never exposed.
func (n *node) Readdir(ctx context.Context) (fusefs.DirStream, syscall.Errno) {
	fh, _, errno := n.OpendirHandle(ctx, 0)
	if errno != 0 {
		return nil, errno
	}
	d := fh.(*dirHandle)
	defer d.Releasedir(ctx, 0)
	var entries []fuse.DirEntry
	for {
		de, errno := d.Readdirent(ctx)
		if errno != 0 {
			return nil, errno
		}
		if de == nil {
			return fusefs.NewListDirStream(entries), 0
		}
		entries = append(entries, *de)
	}
}

// Creating an excluded name fails with EPERM rather than ENOENT: the parent
// directory exists, so "no such file" would be misleading, and creating the
// entry would either touch the hidden source object or produce something
// that is immediately invisible.

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fusefs.Inode, fusefs.FileHandle, uint32, syscall.Errno) {
	if n.hidden(name) {
		return nil, nil, 0, syscall.EPERM
	}
	return n.LoopbackNode.Create(ctx, name, flags, mode, out)
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if n.hidden(name) {
		return nil, syscall.EPERM
	}
	return n.LoopbackNode.Mkdir(ctx, name, mode, out)
}

func (n *node) Mknod(ctx context.Context, name string, mode, rdev uint32, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if n.hidden(name) {
		return nil, syscall.EPERM
	}
	return n.LoopbackNode.Mknod(ctx, name, mode, rdev, out)
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if n.hidden(name) {
		return nil, syscall.EPERM
	}
	return n.LoopbackNode.Symlink(ctx, target, name, out)
}

func (n *node) Link(ctx context.Context, target fusefs.InodeEmbedder, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if n.hidden(name) {
		return nil, syscall.EPERM
	}
	return n.LoopbackNode.Link(ctx, target, name, out)
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.hidden(name) {
		return syscall.ENOENT
	}
	return n.LoopbackNode.Unlink(ctx, name)
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.hidden(name) {
		return syscall.ENOENT
	}
	return n.LoopbackNode.Rmdir(ctx, name)
}

func (n *node) Rename(ctx context.Context, name string, newParent fusefs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	if n.hidden(name) {
		return syscall.ENOENT
	}
	if np.hidden(newName) {
		return syscall.EPERM
	}
	return n.LoopbackNode.Rename(ctx, name, np, newName, flags)
}

// dirHandle wraps the loopback directory handle and drops excluded entries.
type dirHandle struct {
	inner  fusefs.FileHandle
	dir    string
	filter *Filter
}

var (
	_ fusefs.FileReaddirenter = (*dirHandle)(nil)
	_ fusefs.FileSeekdirer    = (*dirHandle)(nil)
	_ fusefs.FileFsyncdirer   = (*dirHandle)(nil)
	_ fusefs.FileReleasedirer = (*dirHandle)(nil)
)

func (d *dirHandle) Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno) {
	r, ok := d.inner.(fusefs.FileReaddirenter)
	if !ok {
		return nil, 0
	}
	for {
		de, errno := r.Readdirent(ctx)
		if errno != 0 || de == nil {
			return de, errno
		}
		if de.Name == "." || de.Name == ".." || !d.filter.ExcludedChild(d.dir, de.Name) {
			return de, 0
		}
	}
}

func (d *dirHandle) Seekdir(ctx context.Context, off uint64) syscall.Errno {
	if s, ok := d.inner.(fusefs.FileSeekdirer); ok {
		return s.Seekdir(ctx, off)
	}
	return syscall.ENOTSUP
}

func (d *dirHandle) Fsyncdir(ctx context.Context, flags uint32) syscall.Errno {
	if s, ok := d.inner.(fusefs.FileFsyncdirer); ok {
		return s.Fsyncdir(ctx, flags)
	}
	return 0
}

func (d *dirHandle) Releasedir(ctx context.Context, flags uint32) {
	if r, ok := d.inner.(fusefs.FileReleasedirer); ok {
		r.Releasedir(ctx, flags)
	}
}
