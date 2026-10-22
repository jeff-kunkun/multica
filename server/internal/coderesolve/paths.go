package coderesolve

import (
	"strings"
)

// Path handling here is deliberately NOT path/filepath. The paths this package
// reasons about belong to the user's machine, which may be Windows while the
// server is Linux: filepath.IsAbs(`C:\Users\x`) is false on Linux, and
// filepath.Join would rewrite the separators of a path the daemon has to match
// literally. So every helper below works on either flavour and preserves the
// separator the path was written with.

// IsAbsolutePath reports whether a path looks absolute on either POSIX or
// Windows. Mirrors handler.isAbsoluteLocalPath, which guards the same strings
// on the way into the database; a row that passed that check passes this one.
func IsAbsolutePath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' {
		return true
	}
	if strings.HasPrefix(s, `\\`) {
		return true
	}
	if len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// separatorOf returns the separator a path is written with. A path holding a
// backslash and no forward slash is Windows; everything else is treated as
// POSIX, which is also the right answer for a Windows path already written
// with forward slashes.
func separatorOf(p string) string {
	if strings.Contains(p, `\`) && !strings.Contains(p, "/") {
		return `\`
	}
	return "/"
}

// trimTrailingSeparators removes trailing separators without eating a root
// ("/" and `C:\` keep theirs).
func trimTrailingSeparators(p string) string {
	for len(p) > 1 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		if len(p) == 3 && isDriveLetter(p[0]) && p[1] == ':' {
			return p
		}
		p = p[:len(p)-1]
	}
	return p
}

// Base returns the last element of a path, treating both separators as such.
func Base(p string) string {
	p = trimTrailingSeparators(p)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Join appends child to parent with parent's separator.
func Join(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	sep := separatorOf(parent)
	trimmed := trimTrailingSeparators(parent)
	if strings.HasSuffix(trimmed, sep) {
		return trimmed + child
	}
	return trimmed + sep + child
}
