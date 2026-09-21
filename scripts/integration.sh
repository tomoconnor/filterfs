#!/usr/bin/env bash
# Copyright (c) 2026 Tom O'Connor <tom@twinhelix.org>
#
# Permission is hereby granted, free of charge, to any person obtaining a copy
# of this software and associated documentation files (the "Software"), to deal
# in the Software without restriction, including without limitation the rights
# to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
# copies of the Software, and to permit persons to whom the Software is
# furnished to do so, subject to the following conditions:
#
# The above copyright notice and this permission notice shall be included in
# all copies or substantial portions of the Software.
#
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
# AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
# LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
# OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
# SOFTWARE.

# End-to-end test of the filterfs binary on Linux.
#
# Usage: scripts/integration.sh [path/to/filterfs]
#
# Exits 0 with a SKIP message if FUSE is not usable here.
set -euo pipefail

BIN=$(realpath "${1:-bin/filterfs}")
[ -x "$BIN" ] || { echo "binary $BIN not found; run 'make build' first" >&2; exit 1; }

if [ "$(uname -s)" != Linux ] || [ ! -e /dev/fuse ]; then
    echo "SKIP: FUSE is not available (needs Linux with /dev/fuse)"
    exit 0
fi
if [ "$(id -u)" != 0 ] && ! command -v fusermount3 >/dev/null && ! command -v fusermount >/dev/null; then
    echo "SKIP: fusermount3 not installed and not running as root"
    exit 0
fi

WORK=$(mktemp -d)
SRC=$WORK/source
MNT=$WORK/view
PID=

unmount() {
    fusermount3 -u "$1" 2>/dev/null || fusermount -u "$1" 2>/dev/null || umount "$1"
}
is_mounted() {
    grep -q " $1 fuse" /proc/self/mounts
}
cleanup() {
    [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
    is_mounted "$MNT" && unmount "$MNT" || true
    rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok   $*"; }

wait_for() { # wait_for <description> <command...>
    local what=$1; shift
    for _ in $(seq 50); do "$@" && return 0; sleep 0.1; done
    fail "timed out waiting for $what"
}

mkdir -p "$SRC/.git" "$SRC/src" "$MNT"
echo "[core]" > "$SRC/.git/config"
echo hello > "$SRC/hello.txt"
echo "SECRET=1" > "$SRC/.env"
echo "package main" > "$SRC/src/main.go"

echo "== background (daemon) mode"
"$BIN" --source "$SRC" --exclude /.git --exclude /.env "$MNT"
is_mounted "$MNT" || fail "not mounted after filterfs returned"
DAEMON=$(pgrep -f -x "$BIN --source $SRC --exclude /.git --exclude /.env $MNT") || fail "daemon not running"
pass "filterfs returned with the filesystem mounted (daemon pid $DAEMON)"

test ! -e "$MNT/.git"       || fail ".git visible";         pass ".git is absent"
test ! -e "$MNT/.git/config" || fail ".git/config visible"; pass ".git/config is absent"
test ! -e "$MNT/.env"       || fail ".env visible";         pass ".env is absent"
! ls -a "$MNT" | grep -qx .git || fail ".git listed";       pass ".git is not listed"
test -f "$MNT/hello.txt"    || fail "hello.txt missing";    pass "hello.txt is visible"
test -f "$MNT/src/main.go"  || fail "src/main.go missing";  pass "src/main.go is visible"

echo changed > "$MNT/hello.txt"
grep -q changed "$SRC/hello.txt" || fail "write through mount not in source"
pass "write through the mount changes the source"

echo source-change > "$SRC/hello.txt"
grep -q source-change "$MNT/hello.txt" || fail "source write not visible in mount"
pass "write to the source is visible through the mount"

! mkdir "$MNT/.git" 2>/dev/null || fail "mkdir .git succeeded"
! touch "$MNT/.git/x" 2>/dev/null || fail "touch .git/x succeeded"
! mv "$MNT/hello.txt" "$MNT/.env" 2>/dev/null || fail "rename onto .env succeeded"
grep -q SECRET "$SRC/.env" || fail "source .env modified"
pass "excluded paths cannot be created or modified"

unmount "$MNT"
wait_for "daemon to exit" bash -c "! kill -0 $DAEMON 2>/dev/null"
is_mounted "$MNT" && fail "still mounted"
pass "unmounted cleanly and the daemon exited"

echo "== foreground mode, SIGTERM"
"$BIN" --foreground --source "$SRC" --exclude /.git "$MNT" &
PID=$!
wait_for "mount" is_mounted "$MNT"
test -f "$MNT/hello.txt" || fail "hello.txt missing"
kill -TERM "$PID"
wait "$PID" || fail "filterfs exited with status $?"
PID=
is_mounted "$MNT" && fail "still mounted after SIGTERM"
pass "SIGTERM unmounts and exits 0"

echo "== foreground mode, SIGINT"
"$BIN" -f --source "$SRC" --exclude /.git "$MNT" &
PID=$!
wait_for "mount" is_mounted "$MNT"
kill -INT "$PID"
wait "$PID" || fail "filterfs exited with status $?"
PID=
is_mounted "$MNT" && fail "still mounted after SIGINT"
pass "SIGINT unmounts and exits 0"

echo "== refuses recursive mounts"
mkdir -p "$SRC/inner"
! "$BIN" -f --source "$SRC" "$SRC" 2>/dev/null || fail "mounted source on itself"
! "$BIN" -f --source "$SRC" "$SRC/inner" 2>/dev/null || fail "mounted inside source"
pass "same-directory and inside-source mountpoints are refused"

echo "== mount.filterfs helper"
HELPER_DIR=$WORK/sbin
mkdir -p "$HELPER_DIR"
ln -s "$BIN" "$HELPER_DIR/mount.filterfs"
"$HELPER_DIR/mount.filterfs" "$SRC" "$MNT" -o rw,exclude=/.git,exclude=/.env
is_mounted "$MNT" || fail "helper did not mount"
test ! -e "$MNT/.git" && test ! -e "$MNT/.env" && test -f "$MNT/hello.txt" || fail "helper mount has wrong contents"
unmount "$MNT"
wait_for "unmount" bash -c "! grep -q ' $MNT fuse' /proc/self/mounts"
pass "mount.filterfs SOURCE MOUNTPOINT -o exclude=... works"

if [ "$(id -u)" = 0 ] && [ -d /sbin ] && [ ! -e /sbin/mount.filterfs ]; then
    ln -s "$BIN" /sbin/mount.filterfs
    trap 'rm -f /sbin/mount.filterfs; cleanup' EXIT
    mount -t filterfs -o exclude=/.git,exclude=/.env "$SRC" "$MNT"
    test ! -e "$MNT/.git" && test -f "$MNT/hello.txt" || fail "mount -t filterfs has wrong contents"
    umount "$MNT"
    wait_for "unmount" bash -c "! grep -q ' $MNT fuse' /proc/self/mounts"
    pass "mount -t filterfs works"
fi

echo "PASS"
