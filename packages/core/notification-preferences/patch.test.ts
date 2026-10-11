import { describe, expect, it } from "vitest";
import {
  applyNotificationPreferencePatch,
  deriveNotificationPreferencePatch,
  rollbackNotificationPreferencePatch,
} from "./patch";

describe("notification preference patches", () => {
  it("persists runtime alerts and rolls back a failed toggle without changing other groups", () => {
    const previous = { comments: "muted" } as const;
    const patch = deriveNotificationPreferencePatch(previous, { ...previous, runtime_alerts: "muted" });
    expect(patch).toEqual({ runtime_alerts: "muted" });
    const muted = applyNotificationPreferencePatch(previous, patch);
    expect(muted).toEqual({ comments: "muted", runtime_alerts: "muted" });
    expect(rollbackNotificationPreferencePatch(muted, patch, previous)).toEqual(previous);
    const enable = deriveNotificationPreferencePatch(muted, previous);
    expect(enable).toEqual({ runtime_alerts: "all" });
    expect(applyNotificationPreferencePatch(muted, enable)).toEqual(previous);
  });
  it("sends only the changed key when another muted preference is present", () => {
    expect(
      deriveNotificationPreferencePatch(
        { status_changes: "muted" },
        { status_changes: "muted", comments: "muted" },
      ),
    ).toEqual({ comments: "muted" });
  });

  it("uses an explicit all value when enabling a previously muted group", () => {
    expect(
      deriveNotificationPreferencePatch(
        { status_changes: "muted", comments: "muted" },
        { comments: "muted" },
      ),
    ).toEqual({ status_changes: "all" });
  });

  it("merges optimistic patches without dropping unrelated preferences", () => {
    expect(
      applyNotificationPreferencePatch(
        { status_changes: "muted" },
        { comments: "muted" },
      ),
    ).toEqual({ status_changes: "muted", comments: "muted" });
  });

  it("carries the mentions group independently of comments", () => {
    // The point of the split (#6468): muting comment volume must not travel
    // to mentions. A missing key in NOTIFICATION_GROUP_KEYS would make the
    // toggle silently un-sendable, so derive and apply are both covered.
    expect(
      deriveNotificationPreferencePatch(
        { comments: "muted" },
        { comments: "muted", mentions: "muted" },
      ),
    ).toEqual({ mentions: "muted" });

    expect(
      deriveNotificationPreferencePatch({ comments: "muted" }, {}),
    ).toEqual({ comments: "all" });

    expect(
      applyNotificationPreferencePatch(
        { comments: "muted" },
        { mentions: "muted" },
      ),
    ).toEqual({ comments: "muted", mentions: "muted" });
  });

  it("does not roll back a key that a later mutation already changed", () => {
    expect(
      rollbackNotificationPreferencePatch(
        { comments: "muted" },
        { comments: "muted" },
        {},
      ),
    ).toEqual({});

    expect(
      rollbackNotificationPreferencePatch(
        {},
        { comments: "muted" },
        {},
      ),
    ).toEqual({});
  });
});
