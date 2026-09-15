package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxAgyLoggedInDirs = 32

var agyCredentialRelativePaths = []string{
	"oauth_creds.json",
	filepath.Join("antigravity-cli", "antigravity-oauth-token"),
}

// probeAgyLoggedInDirs returns absolute Gemini directories under home that
// already hold an AGY/Gemini credential file. The list is host-level metadata
// for the settings UI green check; it never includes token contents.
func probeAgyLoggedInDirs(home string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil
	}
	candidates := []string{filepath.Join(home, ".gemini")}
	if matches, err := filepath.Glob(filepath.Join(home, ".gemini-account*")); err == nil {
		candidates = append(candidates, matches...)
	}
	seen := make(map[string]struct{}, len(candidates))
	var dirs []string
	for _, dir := range candidates {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		if !agyDirHasLogin(dir) {
			continue
		}
		dirs = append(dirs, dir)
		if len(dirs) >= maxAgyLoggedInDirs {
			break
		}
	}
	sort.Strings(dirs)
	return dirs
}

func currentAgyLoggedInDirs() []string {
	home, err := agyQuotaHomeFn()
	if err != nil {
		return nil
	}
	return probeAgyLoggedInDirs(home)
}

func agyDirHasLogin(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, rel := range agyCredentialRelativePaths {
		cred, err := os.Stat(filepath.Join(dir, rel))
		if err == nil && cred.Mode().IsRegular() && cred.Size() > 0 {
			return true
		}
	}
	return false
}
