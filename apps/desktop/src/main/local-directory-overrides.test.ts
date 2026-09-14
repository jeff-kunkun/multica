// @vitest-environment node

import { mkdtemp, readFile } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { describe, expect, it } from "vitest";
import {
  parseLocalDirectoryOverrides,
  readLocalDirectoryOverrides,
  serializeLocalDirectoryOverrides,
  writeLocalDirectorySharedOverride,
} from "./local-directory-overrides";

describe("parseLocalDirectoryOverrides", () => {
  it("reads skip-mutex rows and drops disabled or malformed ones", () => {
    const rows = parseLocalDirectoryOverrides(
      JSON.stringify({
        overrides: [
          {
            daemon_id: "d1",
            local_path: "/Volumes/Stoige/pg-game/",
            skip_path_mutex: true,
          },
          { daemon_id: "d1", local_path: "/tmp/x", skip_path_mutex: false },
          { daemon_id: "", local_path: "/tmp/y", skip_path_mutex: true },
          { local_path: "/tmp/z" },
        ],
      }),
    );
    expect(rows).toEqual([
      { daemonId: "d1", localPath: "/Volumes/Stoige/pg-game" },
    ]);
  });

  it("returns empty for corrupt JSON", () => {
    expect(parseLocalDirectoryOverrides("{not json")).toEqual([]);
  });
});

describe("writeLocalDirectorySharedOverride", () => {
  it("persists an override across a reload and can clear it", async () => {
    const dir = await mkdtemp(join(tmpdir(), "multica-overrides-"));
    const filePath = join(dir, "local-directory-overrides.json");

    await writeLocalDirectorySharedOverride(filePath, {
      daemonId: "d1",
      localPath: "/Volumes/Stoige/pg-game",
      enabled: true,
    });
    const raw = await readFile(filePath, "utf-8");
    expect(JSON.parse(raw)).toEqual({
      overrides: [
        {
          daemon_id: "d1",
          local_path: "/Volumes/Stoige/pg-game",
          skip_path_mutex: true,
        },
      ],
    });
    expect(await readLocalDirectoryOverrides(filePath)).toEqual([
      { daemonId: "d1", localPath: "/Volumes/Stoige/pg-game" },
    ]);

    await writeLocalDirectorySharedOverride(filePath, {
      daemonId: "d1",
      localPath: "/Volumes/Stoige/pg-game",
      enabled: false,
    });
    expect(await readLocalDirectoryOverrides(filePath)).toEqual([]);
  });

  it("round-trips the same JSON the daemon store writes", () => {
    const serialized = serializeLocalDirectoryOverrides([
      { daemonId: "abc", localPath: "/tmp/game" },
    ]);
    expect(parseLocalDirectoryOverrides(serialized)).toEqual([
      { daemonId: "abc", localPath: "/tmp/game" },
    ]);
  });
});
