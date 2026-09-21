package fs

import "testing"

func mustFilter(t *testing.T, paths ...string) *Filter {
	t.Helper()
	f, err := NewFilter(paths)
	if err != nil {
		t.Fatalf("NewFilter(%q): %v", paths, err)
	}
	return f
}

func checkExcluded(t *testing.T, f *Filter, cases map[string]bool) {
	t.Helper()
	for p, want := range cases {
		if got := f.Excluded(p); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestExcludeGit(t *testing.T) {
	checkExcluded(t, mustFilter(t, "/.git"), map[string]bool{
		"/.git":            true,
		"/.git/config":     true,
		"/.git/refs/heads": true,
		"/foo/.git":        false,
		"/foo":             false,
		"/":                false,
		"/.gitignore":      false,
		"/.git2":           false,
	})
}

func TestExcludeDirPrefix(t *testing.T) {
	checkExcluded(t, mustFilter(t, "/foo"), map[string]bool{
		"/foo":         true,
		"/foo/bar":     true,
		"/foo/bar/baz": true,
		"/foobar":      false,
		"/foo.bar":     false,
		"/bar/foo":     false,
	})
}

func TestExcludeNested(t *testing.T) {
	checkExcluded(t, mustFilter(t, "/foo/private"), map[string]bool{
		"/foo":           false,
		"/foo/public":    false,
		"/foo/private":   true,
		"/foo/private/x": true,
		"/private":       false,
	})
}

func TestNormalizationOfQueries(t *testing.T) {
	checkExcluded(t, mustFilter(t, "/.git"), map[string]bool{
		"//.git":             true,
		"/foo/../.git":       true,
		"/foo/../.git/HEAD":  true,
		"/./.git":            true,
		"/.git/":             true,
		"/.git/.":            true,
		".git":               true,
		"/../.git":           true,
		"/../../.git/config": true,
		"/.git/..":           false, // that is "/"
		"/.git/../foo":       false, // that is "/foo"
		"/foo/./.git":        false,
	})
}

func TestNormalizationOfExcludes(t *testing.T) {
	for _, spec := range []string{".git", "/.git/", "//.git", "/foo/../.git", "/./.git", "/../.git"} {
		f := mustFilter(t, spec)
		if got := f.Paths(); len(got) != 1 || got[0] != "/.git" {
			t.Errorf("NewFilter(%q).Paths() = %q, want [/.git]", spec, got)
		}
		if !f.Excluded("/.git/config") {
			t.Errorf("NewFilter(%q) does not exclude /.git/config", spec)
		}
	}
}

func TestInvalidExcludes(t *testing.T) {
	for _, spec := range []string{"", " ", "/", "//", "/.", "/foo/..", "..", "/a\x00b"} {
		if _, err := NewFilter([]string{spec}); err == nil {
			t.Errorf("NewFilter(%q) succeeded, want error", spec)
		}
	}
}

func TestMultipleExcludes(t *testing.T) {
	checkExcluded(t, mustFilter(t, "/.git", "/.env", "/build"), map[string]bool{
		"/.git/HEAD":  true,
		"/.env":       true,
		"/build/out":  true,
		"/src/.env":   false,
		"/README.md":  false,
		"/builder":    false,
		"/src/build":  false,
		"/.envrc":     false,
		"/.git-blame": false,
	})
}

func TestNoExcludes(t *testing.T) {
	f := mustFilter(t)
	if f.Excluded("/.git") {
		t.Error("empty filter excluded /.git")
	}
	var nilFilter *Filter
	if nilFilter.Excluded("/.git") {
		t.Error("nil filter excluded /.git")
	}
}

func TestExcludedChild(t *testing.T) {
	f := mustFilter(t, "/.git", "/foo/private")
	cases := []struct {
		dir, name string
		want      bool
	}{
		{"", ".git", true},
		{"/", ".git", true},
		{"", "src", false},
		{"foo", "private", true},
		{"/foo", "private", true},
		{"foo", "public", false},
		{"foo/private", "x", true},
		{"src", ".git", false},
		// Names that are not a single component must never be
		// resolved: treat them as hidden.
		{"", "", true},
		{"", ".", true},
		{"", "..", true},
		{"foo", "../.git", true},
		{"", "a/b", true},
		{"", "a\x00", true},
	}
	for _, c := range cases {
		if got := f.ExcludedChild(c.dir, c.name); got != c.want {
			t.Errorf("ExcludedChild(%q, %q) = %v, want %v", c.dir, c.name, got, c.want)
		}
	}
}
