// Package platformskill ships the multica-platform skill: Multica's platform
// contracts (issues, mentions, plans, agents, projects, runtimes) behind one
// routing SKILL.md.
//
// Multica does not store or distribute any other skill. Domain skills live in
// each project folder and general skills are installed per user, both outside
// Multica. This one is the exception because its content describes this
// build's own contracts, so the daemon installs it once into the user-level
// skill directories every coding CLI scans (see Install) instead of writing it
// into every task.
package platformskill

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Name is the skill's directory and frontmatter name. The runtime brief points
// agents at it by this name.
const Name = "multica-platform"

// managedMarker marks a directory this package wrote. Install only replaces a
// directory carrying it, so a same-named skill the user installed by hand is
// never overwritten.
const managedMarker = ".multica-managed"

//go:embed multica-platform
var skillFS embed.FS

// File is one file of the skill, addressed relative to the skill directory.
type File struct {
	Path    string
	Content string
}

// Files returns every file of the skill, SKILL.md first.
func Files() []File {
	files := []File{}
	_ = fs.WalkDir(skillFS, Name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := fs.ReadFile(skillFS, p)
		if readErr != nil {
			return readErr
		}
		rel := strings.TrimPrefix(p, Name+"/")
		f := File{Path: rel, Content: string(data)}
		if rel == "SKILL.md" {
			files = append([]File{f}, files...)
		} else {
			files = append(files, f)
		}
		return nil
	})
	return files
}

// UserSkillDirs are the user-level skill directories, relative to the home
// directory, that the supported coding CLIs scan. The first is canonical and
// holds the files; the others link to it.
var UserSkillDirs = []string{
	filepath.Join(".agents", "skills"),
	filepath.Join(".claude", "skills"),
	filepath.Join(".codex", "skills"),
}

// Install writes the skill into home's canonical user-level skill directory
// and links it from the other CLI directories. It is idempotent: a managed
// copy is rewritten so it always matches this build, an existing correct link
// is left alone, and a same-named directory or link the user owns is skipped
// and reported in the returned error without stopping the rest.
func Install(home string) error {
	if home == "" {
		return errors.New("platformskill: empty home directory")
	}
	canonical := filepath.Join(home, UserSkillDirs[0], Name)
	if err := writeManaged(canonical); err != nil {
		return err
	}
	var errs []error
	for _, dir := range UserSkillDirs[1:] {
		if err := ensureLink(filepath.Join(home, dir, Name), canonical); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeManaged(dir string) error {
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || !isManaged(dir) {
			return fmt.Errorf("platformskill: %s exists and was not written by Multica; leaving it", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("platformskill: replace %s: %w", dir, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("platformskill: stat %s: %w", dir, err)
	}
	for _, f := range Files() {
		target := filepath.Join(dir, filepath.FromSlash(path.Clean(f.Path)))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("platformskill: %w", err)
		}
		if err := os.WriteFile(target, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("platformskill: %w", err)
		}
	}
	return os.WriteFile(filepath.Join(dir, managedMarker), []byte("Written by the Multica daemon; replaced on every start.\n"), 0o644)
}

func isManaged(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, managedMarker))
	return err == nil
}

func ensureLink(link, target string) error {
	if dest, err := os.Readlink(link); err == nil {
		if dest == target {
			return nil
		}
		return fmt.Errorf("platformskill: %s links elsewhere (%s); leaving it", link, dest)
	}
	if _, err := os.Lstat(link); err == nil {
		return fmt.Errorf("platformskill: %s exists and was not written by Multica; leaving it", link)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("platformskill: stat %s: %w", link, err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("platformskill: %w", err)
	}
	if err := os.Symlink(target, link); err != nil {
		return fmt.Errorf("platformskill: link %s: %w", link, err)
	}
	return nil
}
