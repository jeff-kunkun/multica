package service

import (
	"embed"
	"io/fs"
	"path"
	"strings"
)

//go:embed builtin_skills
var builtinSkillsFS embed.FS

// Redirect stubs for built-ins this server no longer ships, keyed by the name
// an older daemon's runtime brief still points at. See legacyRedirectSkills.
//
//go:embed builtin_skills_legacy
var legacyBuiltinSkillsFS embed.FS

const (
	builtinSkillsRoot       = "builtin_skills"
	legacyBuiltinSkillsRoot = "builtin_skills_legacy"
)

// PlatformSkillName is the built-in skill carrying Multica's platform
// contracts — issues, mentions, agents, projects, runtimes and skill import
// — behind one routing SKILL.md. Every agent receives it.
//
// The daemon's runtime brief names the same skill from its own constant: the
// daemon runs on the user's machine and must not import this package, so the
// two are pinned by the brief's rendered-output tests instead of a shared
// symbol.
const PlatformSkillName = "multica-platform"

// BuiltinSkills returns the platform's built-in skills, embedded at compile
// time. Every agent receives them on top of its workspace-bound
// skills, so they teach platform-wide "how to" workflows that the runtime brief
// intentionally leaves to skills.
//
// Layout: builtin_skills/<name>/SKILL.md plus optional supporting files. The
// <name> directory carries a "multica-" prefix: that is the platform
// namespace, and the brief names built-ins by their bare name on the
// assumption that no workspace skill shares one. Nothing server-side reserves
// the prefix today, so a user could author a skill that sanitizes to the same
// slug and take the bare directory. That is accepted, not handled — when it
// becomes real, the fix is to reject the prefix at skill create/import rather
// than to make every pointer defensive.
//
// legacyRedirects asks for redirect stubs under the names this server has
// stopped shipping. The runtime brief is assembled by the daemon, not the
// server, so a backend deploy cannot rewrite an installed daemon's copy of it:
// a daemon older than the multica-platform merge still tells its agent to read
// `multica-working-on-issues`. Passing true for such a daemon keeps that
// pointer resolvable; the stub carries no contracts of its own, only the new
// location. Callers decide by capability, never by version string.
func (s *TaskService) BuiltinSkills(legacyRedirects bool) []AgentSkillData {
	skills := loadBuiltinSkillDirs(func(string) bool { return true })
	if legacyRedirects {
		skills = append(skills, legacyRedirectSkills()...)
	}
	return skills
}

// legacyRedirectSkills loads the redirect stubs. They live outside
// builtin_skills/ so that nothing ships them by default — a stub is only ever
// correct for a daemon whose brief still names the skill it replaces.
func legacyRedirectSkills() []AgentSkillData {
	return loadSkillDirs(legacyBuiltinSkillsFS, legacyBuiltinSkillsRoot, func(string) bool { return true })
}

// AllBuiltinSkills returns every built-in skill plus the legacy redirect stubs.
// Only the bundle-resolve path uses it: a daemon can only ask to resolve a ref
// it was handed.
func (s *TaskService) AllBuiltinSkills() []AgentSkillData {
	return append(loadBuiltinSkillDirs(func(string) bool { return true }), legacyRedirectSkills()...)
}

func loadBuiltinSkillDirs(include func(name string) bool) []AgentSkillData {
	return loadSkillDirs(builtinSkillsFS, builtinSkillsRoot, include)
}

func loadSkillDirs(fsys embed.FS, root string, include func(name string) bool) []AgentSkillData {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil
	}
	var skills []AgentSkillData
	for _, entry := range entries {
		if !entry.IsDir() || !include(entry.Name()) {
			continue
		}
		if skill, ok := loadBuiltinSkill(fsys, root, entry.Name()); ok {
			skills = append(skills, skill)
		}
	}
	return skills
}

func loadBuiltinSkill(fsys embed.FS, root, name string) (AgentSkillData, bool) {
	dir := path.Join(root, name)
	content, err := fs.ReadFile(fsys, path.Join(dir, "SKILL.md"))
	if err != nil {
		// A skill directory without a SKILL.md is malformed — skip it rather
		// than ship an empty skill.
		return AgentSkillData{}, false
	}
	skill := AgentSkillData{Name: name, Content: string(content)}
	// Any other file in the directory becomes a supporting file, preserving
	// its relative path so subdirectories (e.g. references/issues.md) survive.
	_ = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		rel := strings.TrimPrefix(p, dir+"/")
		if rel == "SKILL.md" {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			return nil
		}
		skill.Files = append(skill.Files, AgentSkillFileData{Path: rel, Content: string(data)})
		return nil
	})
	return skill, true
}
