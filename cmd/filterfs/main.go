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

// Command filterfs mounts a live, filtered passthrough view of a directory.
//
// It can also be installed (or symlinked) as /sbin/mount.filterfs, in which
// case it accepts the mount(8) helper calling convention:
//
//	mount -t filterfs -o exclude=/.git SOURCE MOUNTPOINT
package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tomoconnor/filterfs/internal/config"
	filterfs "github.com/tomoconnor/filterfs/internal/fs"
)

// daemonEnv marks the re-executed background child. Its value is the
// number of the file descriptor used to report mount success to the parent.
const daemonEnv = "_FILTERFS_DAEMON_FD"

func main() {
	log.SetFlags(0)
	log.SetPrefix("filterfs: ")

	parse, usage := config.Parse, config.Usage
	if strings.HasPrefix(filepath.Base(os.Args[0]), "mount.") {
		parse, usage = config.ParseMount, config.MountUsage
	}

	cfg, err := parse(os.Args[1:])
	if errors.Is(err, config.ErrHelp) {
		fmt.Print(usage)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "filterfs: %v\n\n%s", err, usage)
		os.Exit(2)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}

	if os.Getenv(daemonEnv) != "" {
		// Background child: don't keep the caller's directory busy.
		os.Chdir("/")
	}
	if !cfg.Foreground && os.Getenv(daemonEnv) == "" {
		if err := daemonize(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

// run mounts the filesystem and serves it until it is unmounted or the
// process receives SIGINT/SIGTERM.
func run(cfg *config.Config) error {
	server, err := filterfs.Mount(filterfs.Options{
		Source:     cfg.Source,
		Mountpoint: cfg.Mountpoint,
		Excludes:   cfg.Excludes,
		AllowOther: cfg.AllowOther,
		Debug:      cfg.Debug,
	})
	notifyParent(err)
	if err != nil {
		return fmt.Errorf("mount %s: %w", cfg.Mountpoint, err)
	}
	if cfg.Foreground {
		log.Printf("mounted %s on %s (excluding %v)", cfg.Source, cfg.Mountpoint, cfg.Excludes)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigs {
			log.Printf("received %v, unmounting %s", sig, cfg.Mountpoint)
			if err := server.Unmount(); err != nil {
				// Typically EBUSY: keep serving rather than leave a
				// dead mount behind.
				log.Printf("unmount failed, still serving: %v", err)
			}
		}
	}()

	server.Wait()
	return nil
}

// daemonize re-executes this program in a new session with its stdio
// detached, and waits until the child reports whether mounting succeeded.
// This matches what mount(8) and shells expect: the command returns once
// the filesystem is usable, with a meaningful exit status.
func daemonize() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()

	// Keep argv[0] so mount.filterfs mode survives the re-exec.
	cmd := &exec.Cmd{
		Path:        exe,
		Args:        os.Args,
		Env:         append(os.Environ(), daemonEnv+"=3"),
		ExtraFiles:  []*os.File{w}, // becomes fd 3
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if err := cmd.Start(); err != nil {
		w.Close()
		return err
	}
	w.Close()

	msg, _ := io.ReadAll(r)
	if string(msg) == "ok" {
		return cmd.Process.Release()
	}
	cmd.Wait()
	if len(msg) == 0 {
		return errors.New("background process exited before mounting")
	}
	return errors.New(string(msg))
}

// notifyParent tells a waiting daemonize() call how mounting went.
func notifyParent(mountErr error) {
	if os.Getenv(daemonEnv) != "3" {
		return
	}
	os.Unsetenv(daemonEnv)
	f := os.NewFile(3, "notify")
	if mountErr != nil {
		fmt.Fprintf(f, "mount %v", mountErr)
	} else {
		io.WriteString(f, "ok")
	}
	f.Close()
}
