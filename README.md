# filterfs

`filterfs` is a small FUSE filesystem for Linux. It shows a live, read/write
view of an existing directory with some paths hidden.

```text
filterfs = same live underlying files + filtered namespace
```

The typical case is making one project show up inside another without its
`.git`:

```text
~/projects/project-a/              ~/projects/project-b/vendor/project-a/
├── .git/                          ├── go.mod
├── .env              filterfs     ├── README.md
├── go.mod          ───────────▶   └── internal/
├── README.md       hide /.git
└── internal/       hide /.env
```

`vendor/project-a/go.mod` *is* `project-a/go.mod`. It is not a copy. Writes
from either side show up on the other straight away. Hidden paths behave as
if they don't exist: they're left out of directory listings, and looking
them up, running `stat` on them or opening them returns `ENOENT`.

## Install

Requires Go 1.22+ and FUSE 3 (`fuse3` package, which provides `fusermount3`).

```bash
make build            # -> bin/filterfs
sudo make install     # -> /usr/local/bin/filterfs
```

## Usage

```bash
mkdir -p ~/projects/project-b/vendor/project-a

filterfs \
    --source ~/projects/project-a \
    --exclude /.git \
    ~/projects/project-b/vendor/project-a
```

`filterfs` returns once the filesystem is mounted and keeps serving it in
the background. To unmount it:

```bash
fusermount3 -u ~/projects/project-b/vendor/project-a   # as the mounting user
umount ~/projects/project-b/vendor/project-a           # as root
```

Options:

| Option | Meaning |
| --- | --- |
| `--source DIR` | Directory to expose (required). |
| `--exclude PATH` | Path to hide, relative to the mount root, e.g. `/.git`. Can be repeated. |
| `--foreground`, `-f` | Stay in the foreground. SIGINT/SIGTERM unmounts and exits. |
| `--debug`, `-d` | Log every FUSE request. Implies `--foreground`. |
| `--allow-other` | Let other users access the mount. Non-root users need `user_allow_other` in `/etc/fuse.conf`. |

Exclusions are **exact paths** from the mount root. There are no globs.
`/.git` hides `/.git` and everything under it, whether it is a directory, a
file (as in Git worktrees and submodules) or a symlink. It does not hide
`/foo/.git` or `/.gitignore`. Paths are normalized, so `.git`, `//.git`,
`/.git/` and `/foo/../.git` all mean `/.git`.

### `mount -t filterfs`

If the binary is invoked as `mount.filterfs`, it accepts the mount(8)
helper calling convention. To install the symlink
`/sbin/mount.filterfs -> /usr/local/bin/filterfs`:

```bash
sudo make install-mount-helper
```

Then:

```bash
sudo mount -t filterfs \
    -o exclude=/.git,exclude=/.env \
    /home/user/project-a \
    /home/user/project-b/vendor/project-a
```

or in `/etc/fstab`:

```text
/home/user/project-a  /home/user/project-b/vendor/project-a  filterfs  exclude=/.git,exclude=/.env,allow_other,nofail  0  0
```

Supported `-o` options are `exclude=PATH` (repeatable), `allow_other`,
`debug` and `foreground`. Standard options such as `rw`, `defaults`, `noauto`,
`user`, `nofail` and `_netdev` are accepted and ignored. Exclude paths can't
contain commas in this form.

## How excluded paths behave

| Operation | Result |
| --- | --- |
| `readdir` of the parent | Entry omitted |
| `lookup` / `stat` / `open` / `readlink` of the path or anything below it | `ENOENT` |
| `create`/`mkdir`/`unlink`/`rename`/… *inside* an excluded directory | `ENOENT` (the directory doesn't exist) |
| `unlink`/`rmdir`/`rename` *of* an excluded entry | `ENOENT` |
| Creating an entry *at* an excluded path (`mkdir .git`, `touch .env`, `ln -s x .env`, `mv a .env`, …) | `EPERM` |

Creating an entry at an excluded path is refused rather than passed through.
Passing it through would either touch the hidden source object or create
something that disappears immediately. `ENOENT` would be wrong here because
the parent directory does exist.

Everything else is delegated to the source filesystem using go-fuse's
loopback implementation. That covers reads, writes, create, mkdir, unlink,
rmdir, rename, chmod, chown, timestamps, truncate, symlinks, hardlinks,
xattrs and statfs. Sizes, ownership and permissions are the source's own.
The kernel checks permissions (`default_permissions`).

To keep the view live, kernel entry and attribute caching is turned off.
Changes made directly in the source show up immediately, at the cost of
extra lookups.

## How it compares

| | What you get |
| --- | --- |
| **filterfs** | The same live files, with some paths hidden. No copies and no version-control semantics. |
| **Bind mount** | The same live files, but you can't hide anything: `.git` comes along. It also needs root, or a mount namespace. |
| **OverlayFS** | Layers a writable upper directory over a lower one. Writes go through copy-up into the upper layer, so they *don't* reach the original. Hiding things means whiteouts in the upper layer. |
| **Git submodule** | A separate repository pinned to a commit. You get a second checkout with its own `.git`, not the live working tree. |
| **Git subtree** | Copies project-a's history and files into project-b. The copies diverge until they are explicitly synced. |

filterfs does not copy files and is not a version-control mechanism. From
project-b's point of view, `vendor/project-a` is a plain directory of files.
Those files are project-a's working tree.

## Safety notes and limitations

- **Recursive mounts are refused.** filterfs won't start if the mountpoint
  is the source, or inside the source (unless it's inside an excluded path).
  It also won't start if the source is inside the mountpoint, because
  mounting would then hide the source from filterfs itself. Both paths are
  resolved with symlinks followed before these checks.
- **Symlinks are passed through unchanged.** The kernel resolves relative
  symlinks inside the mount, so a symlink `link -> .git` in the source
  resolves to the hidden `/.git` and returns `ENOENT`. Symlinks that leave
  the mount, such as absolute ones or ones with enough `..`, point wherever
  they point in the real filesystem. filterfs doesn't rewrite them.
- **filterfs is not a sandbox.** It controls what is visible *through the
  mount*. Anyone who can read the source directory can still read `.git`
  there directly, and a hardlink in the source to a hidden file exposes that
  file's contents under its other name. An attacker who can write to the
  source tree concurrently could race directory-to-symlink swaps against
  the path-based passthrough operations.
- Linux only. The code also builds on macOS against macFUSE, but that setup
  is untested.
- Not implemented: globs, regexes, include rules, config files and caching.

## Development

```bash
make test                 # unit tests (filter logic, CLI parsing, validation)
make vet
make integration          # needs Linux + /dev/fuse; skips otherwise
make docker-integration   # runs `make integration` in a privileged Linux container
```

`make integration` runs two suites:

- The in-process FUSE tests in `internal/fs/fs_linux_test.go`. These cover
  visibility, blocking of mutations, symlinks, passthrough in both
  directions and ordinary filesystem operations.
- `scripts/integration.sh`, which tests the built binary: daemon mode,
  foreground mode with SIGINT/SIGTERM, refusal of recursive mounts,
  `mount.filterfs` and, when run as root, `mount -t filterfs`.

Layout:

```text
cmd/filterfs/main.go       CLI entry point, daemonizing, signal handling, mount.filterfs detection
internal/config/config.go  argument parsing (both forms) and validation
internal/fs/filter.go      exact-path exclusion logic
internal/fs/node.go        filtering wrapper around go-fuse's LoopbackNode
internal/fs/fs.go          mount setup
```
