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

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	c, err := Parse([]string{"--source", "/src", "--exclude", "/.git", "/mnt", "--exclude=/.env", "-f"})
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{Source: "/src", Mountpoint: "/mnt", Excludes: []string{"/.git", "/.env"}, Foreground: true}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got %+v, want %+v", c, want)
	}

	c, err = Parse([]string{"--source=/src", "--debug", "/mnt"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Debug || !c.Foreground {
		t.Errorf("--debug should imply --foreground: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	for _, args := range [][]string{
		{"/mnt"},                         // no source
		{"--source", "/src"},             // no mountpoint
		{"--source", "/src", "/a", "/b"}, // two mountpoints
		{"--bogus", "--source", "/src", "/mnt"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", args)
		}
	}
}

func TestParseMount(t *testing.T) {
	c, err := ParseMount([]string{"/src", "/mnt", "-o", "rw,exclude=/.git,exclude=/.env,allow_other", "-n"})
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{Source: "/src", Mountpoint: "/mnt", Excludes: []string{"/.git", "/.env"}, AllowOther: true}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got %+v, want %+v", c, want)
	}

	c, err = ParseMount([]string{"-oexclude=/.git,debug", "/src", "/mnt"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Debug || !c.Foreground || len(c.Excludes) != 1 {
		t.Errorf("unexpected config %+v", c)
	}

	for _, args := range [][]string{
		{"/src"},
		{"/src", "/mnt", "-o"},
		{"/src", "/mnt", "-o", "bogus"},
		{"/src", "/mnt", "-o", "exclude"},
		{"/src", "/mnt", "-x"},
	} {
		if _, err := ParseMount(args); err == nil {
			t.Errorf("ParseMount(%q) succeeded, want error", args)
		}
	}
}

func TestValidate(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "src")
	mnt := filepath.Join(tmp, "mnt")
	for _, d := range []string{src, mnt, filepath.Join(src, "sub"), filepath.Join(src, ".git", "mnt"), filepath.Join(mnt, "inner")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(src, filepath.Join(tmp, "srclink")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		src, mnt string
		excludes []string
		errLike  string // "" means success
	}{
		{"ok", src, mnt, []string{"/.git"}, ""},
		{"same dir", src, src, nil, "same directory"},
		{"same dir via symlink", filepath.Join(tmp, "srclink"), src, nil, "same directory"},
		{"same dir via dotdot", src, filepath.Join(src, "sub", ".."), nil, "same directory"},
		{"mount inside source", src, filepath.Join(src, "sub"), nil, "inside the source"},
		{"mount inside source via symlink", src, filepath.Join(tmp, "srclink", "sub"), nil, "inside the source"},
		{"mount inside excluded part of source", src, filepath.Join(src, ".git", "mnt"), []string{"/.git"}, ""},
		{"source inside mount", filepath.Join(mnt, "inner"), mnt, nil, "inside the mountpoint"},
		{"missing source", filepath.Join(tmp, "nope"), mnt, nil, "source"},
		{"source not a dir", filepath.Join(tmp, "file"), mnt, nil, "not a directory"},
		{"bad exclude", src, mnt, []string{"/"}, "root"},
	}
	for _, c := range cases {
		cfg := &Config{Source: c.src, Mountpoint: c.mnt, Excludes: c.excludes}
		err := cfg.Validate()
		switch {
		case c.errLike == "" && err != nil:
			t.Errorf("%s: unexpected error: %v", c.name, err)
		case c.errLike != "" && err == nil:
			t.Errorf("%s: succeeded, want error containing %q", c.name, c.errLike)
		case c.errLike != "" && !strings.Contains(err.Error(), c.errLike):
			t.Errorf("%s: error %q does not contain %q", c.name, err, c.errLike)
		}
	}

	cfg := &Config{Source: filepath.Join(tmp, "srclink"), Mountpoint: mnt}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Source != src {
		t.Errorf("Source not resolved: %q, want %q", cfg.Source, src)
	}
}
