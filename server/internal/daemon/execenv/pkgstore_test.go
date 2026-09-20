package execenv

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPreparePackageStoreCreatesEveryDirectoryUnderTheWorkspacesRoot(t *testing.T) {
	root := t.TempDir()

	env, err := PreparePackageStore(root)
	if err != nil {
		t.Fatalf("PreparePackageStore: %v", err)
	}

	if len(env) == 0 {
		t.Fatal("expected a non-empty environment")
	}
	storeRoot := filepath.Join(root, PackageStoreDirName)
	for key, dir := range env {
		if !strings.HasPrefix(dir, storeRoot+string(os.PathSeparator)) {
			t.Errorf("%s = %q, want a path under %q", key, dir, storeRoot)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("%s: directory not created: %v", key, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s: %q is not a directory", key, dir)
		}
	}
}

// The store only shares bytes with a node_modules on the same filesystem, so
// its whole reason to exist is being anchored to the workspaces root rather
// than to $HOME. Two different roots must therefore never collapse onto one
// store path.
func TestPackageStoreRootFollowsTheWorkspacesRoot(t *testing.T) {
	a := PackageStoreRoot(filepath.Join("/vol-a", "multica_workspaces"))
	b := PackageStoreRoot(filepath.Join("/vol-b", "multica_workspaces"))
	if a == b {
		t.Fatalf("two workspace roots share a store path: %q", a)
	}
	if got := PackageStoreRoot("   "); got != "" {
		t.Errorf("PackageStoreRoot(blank) = %q, want empty", got)
	}
	if got := PnpmStoreDir(""); got != "" {
		t.Errorf("PnpmStoreDir(blank) = %q, want empty", got)
	}
}

func TestPreparePackageStoreWithoutAWorkspacesRootIsANoOp(t *testing.T) {
	env, err := PreparePackageStore("")
	if err != nil {
		t.Fatalf("PreparePackageStore(\"\"): %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("expected no environment, got %v", env)
	}
}

// pnpm is the only package manager here that shares package CONTENT rather
// than just downloads, so npm_config_store_dir is the variable the whole
// feature turns on. PnpmStoreDir must name the same directory the task
// environment does, or the GC would prune a store no task is filling.
func TestPnpmStoreDirMatchesTheInjectedStoreDir(t *testing.T) {
	root := t.TempDir()

	env, err := PreparePackageStore(root)
	if err != nil {
		t.Fatalf("PreparePackageStore: %v", err)
	}

	injected, ok := env["npm_config_store_dir"]
	if !ok {
		t.Fatal("npm_config_store_dir is not set; nothing points pnpm at the shared store")
	}
	if got := PnpmStoreDir(root); got != injected {
		t.Fatalf("PnpmStoreDir = %q, npm_config_store_dir = %q; the GC would prune the wrong directory", got, injected)
	}
}

// The npm_config_* overrides are only honoured in lowercase by pnpm and yarn
// classic. A well-meant rename to NPM_CONFIG_* would silently stop redirecting
// them while npm alone kept working.
func TestPackageStoreEnvKeysKeepTheirExactSpelling(t *testing.T) {
	keys := PackageStoreEnvKeys()
	for _, want := range []string{
		"npm_config_store_dir",
		"npm_config_cache_dir",
		"npm_config_cache",
		"YARN_CACHE_FOLDER",
		"BUN_INSTALL_CACHE_DIR",
		"GOMODCACHE",
		"PIP_CACHE_DIR",
		"UV_CACHE_DIR",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("PackageStoreEnvKeys() is missing %q; got %v", want, keys)
		}
	}
	// GOCACHE and CARGO_HOME are excluded on purpose — see packageStoreLayout.
	for _, unwanted := range []string{"GOCACHE", "CARGO_HOME"} {
		if slices.Contains(keys, unwanted) {
			t.Errorf("PackageStoreEnvKeys() unexpectedly sets %q", unwanted)
		}
	}
}

// A task deleting or rewriting its own node_modules must not reach the store.
// Nothing in the store's path layout is task-scoped, so the guarantee is that
// the store lives outside every task directory.
func TestPackageStoreIsNotInsideAnyTaskDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := PreparePackageStore(root); err != nil {
		t.Fatalf("PreparePackageStore: %v", err)
	}

	taskDir := filepath.Join(root, "workspace-1234", "issue-abcd")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatalf("mkdir task dir: %v", err)
	}
	store := PackageStoreRoot(root)
	if strings.HasPrefix(store, taskDir) {
		t.Fatalf("store %q sits inside task dir %q; a task wipe would take the store with it", store, taskDir)
	}
}

// Every daemon walk over the workspaces root decides what to do with sibling
// caches by their leading dot (.repos, .skill-cache). The store has to answer
// that check the same way or the GC would start treating it as a workspace.
func TestPackageStoreDirNameIsDotPrefixed(t *testing.T) {
	if !strings.HasPrefix(PackageStoreDirName, ".") {
		t.Fatalf("PackageStoreDirName = %q, want a dot-prefixed sibling cache name", PackageStoreDirName)
	}
	if strings.ContainsAny(PackageStoreDirName, `/\`) {
		t.Fatalf("PackageStoreDirName = %q, want a single path segment", PackageStoreDirName)
	}
}

func TestPreparePackageStoreReportsAnUnusableRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not block directory creation the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unwritable directory is still writable")
	}
	root := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := PreparePackageStore(root); err == nil {
		t.Fatal("expected an error for a read-only workspaces root")
	}
}
