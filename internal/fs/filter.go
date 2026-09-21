package fs

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Filter decides whether a path, relative to the root of the mounted
// filesystem, is hidden. A path is hidden if it, or any of its ancestors,
// is one of the configured exclusions.
type Filter struct {
	excluded map[string]struct{}
}

// NewFilter builds a Filter from exact exclusion paths such as "/.git".
// Paths are normalized with Normalize; excluding the root is an error.
func NewFilter(paths []string) (*Filter, error) {
	f := &Filter{excluded: make(map[string]struct{}, len(paths))}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return nil, fmt.Errorf("empty exclude path")
		}
		if strings.ContainsRune(p, 0) {
			return nil, fmt.Errorf("exclude path %q contains a NUL byte", p)
		}
		n := Normalize(p)
		if n == "/" {
			return nil, fmt.Errorf("exclude path %q refers to the filesystem root", p)
		}
		f.excluded[n] = struct{}{}
	}
	return f, nil
}

// Normalize turns p into a clean absolute path relative to the mount root.
// "foo", "/foo/", "//foo", "/bar/../foo" and "/../foo" all become "/foo":
// ".." can never climb above the root.
func Normalize(p string) string {
	return path.Clean("/" + p)
}

// Excluded reports whether p, or any ancestor of p, is excluded.
func (f *Filter) Excluded(p string) bool {
	if f == nil || len(f.excluded) == 0 {
		return false
	}
	for p = Normalize(p); p != "/"; p = path.Dir(p) {
		if _, ok := f.excluded[p]; ok {
			return true
		}
	}
	return false
}

// ExcludedChild reports whether the entry name inside directory dir is
// excluded. name must be a single path component; anything else (".",
// "..", or a name containing "/") is treated as excluded so it can never
// be used to reach a different path than the one checked.
func (f *Filter) ExcludedChild(dir, name string) bool {
	if !validName(name) {
		return true
	}
	return f.Excluded(path.Join("/", dir, name))
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsRune(name, '/') && !strings.ContainsRune(name, 0)
}

// Paths returns the normalized exclusions, for logging.
func (f *Filter) Paths() []string {
	out := make([]string, 0, len(f.excluded))
	for p := range f.excluded {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
