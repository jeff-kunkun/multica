// A `vX.Y.Z-test.N` tag is the Desktop test channel, and the GitHub release it
// creates MUST be flagged as a pre-release.
//
// Stable clients run electron-updater with `allowPrerelease` off, where the
// GitHub provider resolves its tag through `GET /releases/latest` — an endpoint
// that skips pre-releases and nothing else. Publish a test release without the
// flag and every stable client resolves to it, asks for a `latest*.yml` that
// release does not carry, and dies with ERR_UPDATER_CHANNEL_FILE_NOT_FOUND.
// One un-flagged test tag is enough to take the whole stable line down.
//
// Both release workflows create the release from the same tag push and either
// can win that race, so the rule is asserted against both — by running their
// actual `gh release create` block against a stub `gh` rather than grepping
// the YAML for a flag that may or may not reach the command.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

const WORKFLOWS = [
  ".github/workflows/desktop-release.yml",
  ".github/workflows/cli-release.yml",
];
const STEP_NAME = "Ensure the release exists";

/**
 * Pull one step's `run: |` block out of a workflow and dedent it. Reading the
 * YAML by hand keeps this test free of a parser dependency; the block is
 * plain bash with no `${{ }}` expressions, so it runs as-is.
 */
function runBlockFor(workflow) {
  const lines = readFileSync(workflow, "utf-8").split("\n");
  const start = lines.findIndex((line) => line.trim() === `- name: ${STEP_NAME}`);
  assert.ok(start >= 0, `${workflow} has no "${STEP_NAME}" step`);
  const runAt = lines.findIndex((line, i) => i > start && line.trim() === "run: |");
  assert.ok(runAt > start, `${workflow}'s "${STEP_NAME}" step has no run block`);

  const body = [];
  const indent = lines[runAt + 1].length - lines[runAt + 1].trimStart().length;
  for (const line of lines.slice(runAt + 1)) {
    if (line.trim() !== "" && line.search(/\S/) < indent) break;
    body.push(line.slice(indent));
  }
  const script = body.join("\n");
  assert.ok(
    !script.includes("${{"),
    `${workflow}'s "${STEP_NAME}" block interpolates Actions expressions; this test cannot run it`,
  );
  return script;
}

/**
 * Run the block with a stub `gh` that reports the release as missing, so the
 * create path is the one exercised, and records the argv it was called with.
 */
function ghCreateArgs(script, tag) {
  const dir = mkdtempSync(join(tmpdir(), "release-prerelease-"));
  try {
    const log = join(dir, "gh.log");
    const gh = join(dir, "gh");
    writeFileSync(
      gh,
      [
        "#!/usr/bin/env bash",
        // `release view` answering non-zero is "this release does not exist
        // yet", the state the create path is for.
        'for arg in "$@"; do [[ "$arg" == view ]] && exit 1; done',
        'printf "%s\\n" "$@" >> "$GH_LOG"',
        "exit 0",
      ].join("\n"),
      "utf-8",
    );
    chmodSync(gh, 0o755);
    writeFileSync(join(dir, "step.sh"), script, "utf-8");

    execFileSync("bash", [join(dir, "step.sh")], {
      encoding: "utf-8",
      env: {
        ...process.env,
        PATH: `${dir}:${process.env.PATH}`,
        GH_LOG: log,
        GH_TOKEN: "stub",
        REPO: "jeff-kunkun/multica",
        TAG: tag,
      },
    });
    return readFileSync(log, "utf-8").split("\n").filter(Boolean);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

for (const workflow of WORKFLOWS) {
  const script = runBlockFor(resolve(workflow));

  test(`${workflow} flags a -test.N release as a pre-release`, () => {
    for (const tag of ["v0.5.5-test.1", "v0.5.5-test.12", "v1.0.0-test.1"]) {
      const args = ghCreateArgs(script, tag);
      assert.ok(args.includes("create"), `${tag}: expected a release create`);
      assert.ok(
        args.includes("--prerelease"),
        `${tag}: created without --prerelease, which takes the stable channel down`,
      );
    }
  });

  test(`${workflow} leaves a stable release unflagged`, () => {
    for (const tag of ["v0.5.5", "v1.0.0", "v0.5.10"]) {
      const args = ghCreateArgs(script, tag);
      assert.ok(args.includes("create"), `${tag}: expected a release create`);
      assert.ok(
        !args.includes("--prerelease"),
        `${tag}: a stable tag marked pre-release disappears from /releases/latest`,
      );
      assert.ok(
        !args.includes(""),
        `${tag}: an empty argument reached gh; the flag must expand to nothing, not to ""`,
      );
    }
  });
}
