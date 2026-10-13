// @vitest-environment node
import { describe, expect, it } from "vitest";

import { descriptionPreview } from "./description-preview";


describe("descriptionPreview", () => {
  it("still flattens ordinary markdown to a single line", () => {
    expect(
      descriptionPreview("# Title\n\n**bold** text and [a link](https://example.com)"),
    ).toBe("Title bold text and a link");
  });

  // The editor serializes link brackets escaped. Without unescaping first, the
  // link rule leaves the leading `\` untouched and captures the trailing one,
  // so the preview reads `\repro script\`.
  it("unwraps a link whose brackets the editor escaped", () => {
    expect(descriptionPreview("See \\[repro script\\](https://example.com)")).toBe(
      "See repro script",
    );
  });

  it("still unwraps a link written without escapes", () => {
    expect(descriptionPreview("See [repro script](https://example.com)")).toBe(
      "See repro script",
    );
  });

  // The editor autolinks a raw URL inside a link label, producing a nested
  // link destination like `([https://x](https://x))`. A destination pattern
  // that stops at the first `)` leaves the outer paren stranded in the
  // preview.
  it("unwraps a link whose destination is itself a nested link", () => {
    expect(
      descriptionPreview(
        "This is **very** flaky and needs a \\[repro script\\]([https://example.com](https://example.com)) before we fix it.",
      ),
    ).toBe("This is very flaky and needs a repro script before we fix it.");
  });

  it("unwraps a link whose destination legitimately contains parentheses", () => {
    expect(
      descriptionPreview("See [wiki](https://en.wikipedia.org/wiki/Foo_(bar)) for details"),
    ).toBe("See wiki for details");
  });

  it("drops the backslashes from escaped emphasis and punctuation", () => {
    expect(descriptionPreview("it costs \\$5 \\*not\\* \\$10")).toBe("it costs $5 not $10");
  });

  // A backslash before a letter is not a Markdown escape, so prose keeps it.
  it("leaves a literal backslash in prose alone", () => {
    expect(descriptionPreview("open C:\\Users\\zain to continue")).toBe(
      "open C:\\Users\\zain to continue",
    );
  });

  // The board mounts one preview per card, so an unbounded description makes
  // every card carry text the clamp will never show. (#6633)
  it("bounds a long description before it reaches the DOM", () => {
    const preview = descriptionPreview("a".repeat(600));

    expect(preview).toBe(`${"a".repeat(299)}…`);
    expect(preview).toHaveLength(300);
  });

  it("does not split a Unicode surrogate pair at the preview boundary", () => {
    expect(descriptionPreview(`${"a".repeat(298)}🙂${"b".repeat(300)}`)).toBe(
      `${"a".repeat(298)}…`,
    );
  });

  it("leaves a description at the boundary untouched", () => {
    expect(descriptionPreview("a".repeat(300))).toBe("a".repeat(300));
  });
});
