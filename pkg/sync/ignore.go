package sync

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreRules holds parsed ignore patterns.
type IgnoreRules struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern  string
	negated  bool
	dirOnly  bool
}

// LoadIgnoreFile reads a .izeropignore file and returns parsed rules.
func LoadIgnoreFile(syncDir string) *IgnoreRules {
	rules := &IgnoreRules{}

	path := filepath.Join(syncDir, ".izeropignore")
	f, err := os.Open(path)
	if err != nil {
		return rules // no ignore file = no rules
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p := ignorePattern{}

		// Negation
		if strings.HasPrefix(line, "!") {
			p.negated = true
			line = line[1:]
		}

		// Directory-only pattern
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		p.pattern = line
		rules.patterns = append(rules.patterns, p)
	}

	return rules
}

// IsIgnored checks if a relative path should be ignored.
// isDir indicates whether the path is a directory.
func (r *IgnoreRules) IsIgnored(relPath string, isDir bool) bool {
	if len(r.patterns) == 0 {
		return false
	}

	// Normalize to forward slashes
	relPath = filepath.ToSlash(relPath)
	name := filepath.Base(relPath)

	ignored := false
	for _, p := range r.patterns {
		if p.dirOnly && !isDir {
			continue
		}

		matched := matchPattern(p.pattern, relPath, name)
		if matched {
			if p.negated {
				ignored = false
			} else {
				ignored = true
			}
		}
	}

	return ignored
}

// matchPattern checks if a pattern matches a path.
// Patterns without "/" match against the basename only.
// Patterns with "/" match against the full relative path.
func matchPattern(pattern, relPath, name string) bool {
	// If pattern contains a slash (not just trailing), match full path
	if strings.Contains(pattern, "/") {
		matched, _ := filepath.Match(pattern, relPath)
		if matched {
			return true
		}
		// Also try matching with ** expansion
		return matchDoublestar(pattern, relPath)
	}

	// No slash — match against basename
	matched, _ := filepath.Match(pattern, name)
	if matched {
		return true
	}

	// Also try matching against the full path (for patterns like *.log)
	matched, _ = filepath.Match(pattern, relPath)
	return matched
}

// matchDoublestar handles ** patterns (match any number of directories).
func matchDoublestar(pattern, path string) bool {
	if !strings.Contains(pattern, "**") {
		return false
	}

	parts := strings.Split(pattern, "**")
	if len(parts) != 2 {
		return false // only support single **
	}

	prefix := parts[0]
	suffix := parts[1]

	// Remove leading/trailing slashes from suffix
	suffix = strings.TrimPrefix(suffix, "/")

	if prefix != "" && !strings.HasPrefix(path, prefix) {
		return false
	}

	if suffix == "" {
		return true
	}

	// Check if any suffix of path matches the suffix pattern
	remaining := path
	if prefix != "" {
		remaining = strings.TrimPrefix(path, prefix)
	}

	// Walk through path segments looking for suffix match
	segments := strings.Split(remaining, "/")
	for i := range segments {
		subpath := strings.Join(segments[i:], "/")
		matched, _ := filepath.Match(suffix, subpath)
		if matched {
			return true
		}
		// Also match just the last segment
		if i == len(segments)-1 {
			matched, _ = filepath.Match(suffix, segments[i])
			if matched {
				return true
			}
		}
	}

	return false
}
