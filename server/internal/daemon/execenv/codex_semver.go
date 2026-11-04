package execenv

import (
	"fmt"
	"regexp"
	"strconv"
)

// Small semver helper, scoped to this package to avoid an import cycle with
// server/pkg/agent. Upstream keeps it in codex_sandbox.go; the office removed
// the sandbox version gate there (DENE-2667 PR 7), so the helper lives here
// for the catalog-v2 multi-agent check (MUL-8400) that still needs it.

type codexSemver struct {
	Major, Minor, Patch int
}

var codexSemverRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

func parseCodexSemver(raw string) (codexSemver, error) {
	m := codexSemverRe.FindStringSubmatch(raw)
	if m == nil {
		return codexSemver{}, fmt.Errorf("cannot parse version %q", raw)
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return codexSemver{Major: maj, Minor: min, Patch: pat}, nil
}

func (v codexSemver) lessThan(o codexSemver) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}
