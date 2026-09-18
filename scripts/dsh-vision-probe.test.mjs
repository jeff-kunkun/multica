#!/usr/bin/env node
// Offline coverage for scripts/dsh-vision-probe.mjs.
// Never opens a network connection or reads a real ~/.dsh.
//
// Run: node scripts/dsh-vision-probe.test.mjs

import assert from "node:assert/strict";
import {
  VERDICT,
  applyModalities,
  buildProbeImage,
  classifyAnswer,
  classifyError,
  decisionsFrom,
  dshHome,
  parseProviders,
  probeModel,
  readCredentialRefs,
} from "./dsh-vision-probe.mjs";

const SETTINGS = `agent-default-model:
    provider: opencode2
    model: deepseek-v4.1-flash
llm-pi-ai:
    providers:
        opencode2:
            apiKeyEnv: MULTICA_OPENCODE2_API_KEY
            displayName: "OpenCode Go"
            api: openai-completions
            baseURL: https://opencode.ai/zen/go/v1
            headers:
                x-opencode-session: ses_multica_dsh_b1
            models:
                - id: sees-images
                  name: "Sees Images"
                  contextWindow: 1000000
                - id: text-only
                  name: "Text Only"
                  input: ["text", "image"]
                  contextWindow: 1000000
                - id: silently-blind
                  name: "Silently Blind"
                  inputModalities: ["text", "image"]
        other:
            apiKeyEnv: OTHER_KEY
            baseURL: https://example.invalid/v1
            models:
                - id: sees-images
llm-deepseek:
    providers:
        native:
            apiKeyEnv: NATIVE_KEY
            baseURL: https://api.deepseek.example/v1
            models:
                - id: native-model
                  name: "Native"
`;

function testParse() {
  const providers = parseProviders(SETTINGS);
  assert.equal(providers.length, 3, "providers under every plugin section are found");
  const [zen, other, native] = providers;
  // The key that actually takes effect is decided by the owning plugin.
  assert.equal(zen.modalityKey, "input");
  assert.equal(native.modalityKey, "inputModalities");
  assert.equal(native.name, "native");
  assert.equal(zen.name, "opencode2");
  assert.equal(zen.apiKeyEnv, "MULTICA_OPENCODE2_API_KEY");
  assert.equal(zen.baseURL, "https://opencode.ai/zen/go/v1");
  // The session header is not optional on the Go endpoint — losing it in the
  // parse turns every probe into a 400 MissingSessionID that looks like a
  // capability answer.
  assert.deepEqual(zen.headers, { "x-opencode-session": "ses_multica_dsh_b1" });
  assert.deepEqual(
    zen.models.map((m) => m.id),
    ["sees-images", "text-only", "silently-blind"],
  );
  assert.equal(zen.models[0].declared, false, "an absent field means text-only");
  assert.equal(zen.models[1].declared, true, "input: is what llm-pi-ai reads");
  // The whole DENE-591 trap: a well-formed `inputModalities` under a pi-ai
  // provider is dropped by the schema, so the model is still blind.
  assert.equal(zen.models[2].declared, false, "inputModalities is inert under llm-pi-ai");
  assert.deepEqual(zen.models[2].strayLines.length, 1, "but the dead line is still tracked for cleanup");
  assert.equal(other.name, "other");
  assert.equal(other.models.length, 1);
  // displayName / name / api must not be mistaken for a header.
  assert.equal(Object.keys(other.headers).length, 0);
}

function testApply() {
  const decisions = new Map([
    ["opencode2/sees-images", true],
    ["opencode2/text-only", false],
    ["opencode2/silently-blind", false],
  ]);
  const { text, changed } = applyModalities(SETTINGS, decisions);
  assert.equal(changed, 3);
  const lines = text.split("\n");
  const at = lines.findIndex((l) => l.includes("- id: sees-images"));
  assert.match(lines[at + 1], /^ {18}input: \["text", "image"\]$/, "declared with the key the plugin reads");
  assert.ok(!text.includes(`input: ["text", "image"]\n                  contextWindow`), "text-only withdrawn");
  // The inert line under the other plugin's key is cleaned up rather than left
  // to spring to life if the provider ever moves plugins.
  assert.equal(text.includes("inputModalities"), false);
  // The provider filtered out of the run keeps its own entry untouched.
  assert.ok(text.includes("        other:"));

  // Re-running with the same verdicts writes nothing: the probe is a
  // maintenance command, so a no-op run must not churn the user's file or
  // leave a backup behind.
  const second = applyModalities(text, decisions);
  assert.equal(second.changed, 0);
  assert.equal(second.text, text);

  // A provider-level default that already carries image cannot be withdrawn by
  // deleting a line — the model would inherit image right back.
  const withDefault = SETTINGS.replace(
    "            baseURL: https://opencode.ai/zen/go/v1\n",
    "            baseURL: https://opencode.ai/zen/go/v1\n            defaultInput: [\"text\", \"image\"]\n",
  );
  const pinned = applyModalities(withDefault, new Map([["opencode2/sees-images", false]]));
  assert.ok(pinned.text.includes(`                  input: ["text"]`), "blind model pinned to text");

  // A model with no verdict (quota, dead) is never touched in either direction.
  const partial = applyModalities(SETTINGS, new Map());
  assert.equal(partial.changed, 0);
  assert.equal(partial.text, SETTINGS);
}

function testClassifyAnswer() {
  assert.equal(classifyAnswer("top=red, bottom=blue"), true);
  assert.equal(classifyAnswer("The top half is RED and the bottom is Blue."), true);
  // Order matters: an inverted answer is not a model that saw the image.
  assert.equal(classifyAnswer("top=blue, bottom=red"), false);
  assert.equal(classifyAnswer("I can't view the image."), false);
  assert.equal(classifyAnswer(""), false);
  assert.equal(classifyAnswer(null), false);
}

function testClassifyError() {
  assert.equal(
    classifyError(400, '{"error":{"message":"Model only supports text input; received unsupported content type \'image_url\'."}}'),
    VERDICT.refused,
  );
  assert.equal(
    classifyError(400, '{"error":{"message":"This model does not support image inputs"}}'),
    VERDICT.refused,
  );
  assert.equal(
    classifyError(400, '{"error":{"message":"No endpoints found that support image input"}}'),
    VERDICT.refused,
  );
  assert.equal(
    classifyError(400, '{"error":{"message":"The provided messages input is invalid. [Unexpected item type in content.]"}}'),
    VERDICT.refused,
  );
  assert.equal(
    classifyError(429, '{"error":{"type":"GoUsageLimitError","message":"Weekly usage limit reached."}}'),
    VERDICT.quota,
  );
  assert.equal(classifyError(500, '{"error":{"message":"Internal server error"}}'), VERDICT.unknown);
}

function fakeFetch(handler) {
  return async (url, init) => {
    const body = JSON.parse(init.body);
    const { status, payload } = handler(body, url, init);
    return { status, text: async () => (typeof payload === "string" ? payload : JSON.stringify(payload)) };
  };
}

const PROVIDER = {
  name: "opencode2",
  baseURL: "https://opencode.ai/zen/go/v1",
  headers: { "x-opencode-session": "ses_test" },
};

async function testProbeVerdicts() {
  const seen = { auth: null, session: null, url: null };
  const vision = await probeModel({
    provider: PROVIDER,
    model: "sees-images",
    apiKey: "sk-secret",
    imageB64: "AAAA",
    fetchImpl: fakeFetch((body, url, init) => {
      seen.url = url;
      seen.auth = init.headers.Authorization;
      seen.session = init.headers["x-opencode-session"];
      const parts = body.messages[0].content;
      assert.equal(parts[1].type, "image_url");
      return { status: 200, payload: { choices: [{ message: { content: "top=red, bottom=blue" } }] } };
    }),
  });
  assert.equal(vision.verdict, VERDICT.vision);
  assert.equal(seen.url, "https://opencode.ai/zen/go/v1/chat/completions");
  assert.equal(seen.auth, "Bearer sk-secret");
  assert.equal(seen.session, "ses_test");

  const refused = await probeModel({
    provider: PROVIDER,
    model: "text-only",
    apiKey: "k",
    imageB64: "AAAA",
    fetchImpl: fakeFetch(() => ({
      status: 400,
      payload: { error: { message: "Model only supports text input; received unsupported content type 'image_url'." } },
    })),
  });
  assert.equal(refused.verdict, VERDICT.refused);

  // The case a human reading HTTP status codes gets wrong: 200, no error, and
  // an answer written without ever seeing the picture.
  const blind = await probeModel({
    provider: PROVIDER,
    model: "silently-blind",
    apiKey: "k",
    imageB64: "AAAA",
    fetchImpl: fakeFetch(() => ({
      status: 200,
      payload: { choices: [{ message: { content: "I don't have access to the image." } }] } ,
    })),
  });
  assert.equal(blind.verdict, VERDICT.blind);

  // A model that is down answers 500 to text too — modality is unknown, and
  // filing it as text-only would withdraw a correct declaration.
  let calls = 0;
  const dead = await probeModel({
    provider: PROVIDER,
    model: "down",
    apiKey: "k",
    imageB64: "AAAA",
    attempts: 2,
    retryDelayMs: 0,
    fetchImpl: fakeFetch(() => {
      calls++;
      return { status: 500, payload: { error: { message: "Internal server error" } } };
    }),
  });
  assert.equal(dead.verdict, VERDICT.dead);
  assert.equal(calls, 4, "both the image probe and the text control retry a 5xx");

  // A model that keeps 5xx-ing on images while answering text is not proven
  // text-only — nothing may be withdrawn on that evidence.
  const flaky = await probeModel({
    provider: PROVIDER,
    model: "flaky",
    apiKey: "k",
    imageB64: "AAAA",
    attempts: 2,
    retryDelayMs: 0,
    fetchImpl: fakeFetch((body) =>
      Array.isArray(body.messages[0].content)
        ? { status: 500, payload: { error: { message: "Internal server error" } } }
        : { status: 200, payload: { choices: [{ message: { content: "pong" } }] } },
    ),
  });
  assert.equal(flaky.verdict, VERDICT.unknown);

  // One flaky 500 followed by a real answer must come back as vision: the
  // retry exists so a busy endpoint cannot withdraw a correct declaration.
  let attemptsSeen = 0;
  const transient = await probeModel({
    provider: PROVIDER,
    model: "busy",
    apiKey: "k",
    imageB64: "AAAA",
    attempts: 3,
    retryDelayMs: 0,
    fetchImpl: fakeFetch(() => {
      attemptsSeen++;
      return attemptsSeen === 1
        ? { status: 500, payload: { error: { message: "Internal server error" } } }
        : { status: 200, payload: { choices: [{ message: { content: "top=red, bottom=blue" } }] } };
    }),
  });
  assert.equal(transient.verdict, VERDICT.vision);
  assert.equal(attemptsSeen, 2);

  let quotaCalls = 0;
  const quota = await probeModel({
    provider: PROVIDER,
    model: "capped",
    apiKey: "k",
    imageB64: "AAAA",
    attempts: 3,
    retryDelayMs: 0,
    fetchImpl: fakeFetch(() => {
      quotaCalls++;
      return {
        status: 429,
        payload: { error: { type: "GoUsageLimitError", message: "Weekly usage limit reached." } },
      };
    }),
  });
  assert.equal(quota.verdict, VERDICT.quota);
  assert.equal(quotaCalls, 1, "a 429 is already conclusive; it must not spend a second call");
}

function testCredentials() {
  const refs = readCredentialRefs(`version: 1
refs:
    MULTICA_OPENCODE2_API_KEY: sk-abc
    COMMAND_CODE_API_KEY: user_xyz
records:
    client-connection/browser-session:
        kind: grant
`);
  assert.deepEqual(refs, { MULTICA_OPENCODE2_API_KEY: "sk-abc", COMMAND_CODE_API_KEY: "user_xyz" });
}

function testDshHome() {
  assert.equal(dshHome({ DSH_HOME: "/custom/dsh" }, "/home/u"), "/custom/dsh");
  assert.equal(dshHome({ DSH_HOME: "  " }, "/home/u"), "/home/u/.dsh");
  assert.equal(dshHome({}, "/home/u"), "/home/u/.dsh");
}

function testProbeImage() {
  const png = buildProbeImage();
  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  assert.ok(png.includes(Buffer.from("IHDR")));
  assert.ok(png.includes(Buffer.from("IEND")));
}

function testDecisionsFrom() {
  const providers = [
    {
      name: "opencode1",
      baseURL: "https://opencode.ai/zen/go/v1",
      models: [{ id: "sees-images" }, { id: "text-only" }, { id: "elsewhere-only" }],
    },
    {
      name: "opencode2",
      baseURL: "https://opencode.ai/zen/go/v1/",
      models: [{ id: "sees-images" }, { id: "text-only" }],
    },
    {
      name: "command-code",
      baseURL: "https://api.commandcode.ai/provider/v1",
      models: [{ id: "sees-images" }],
    },
  ];
  const results = [
    { provider: "opencode2", model: "sees-images", verdict: VERDICT.vision },
    { provider: "opencode2", model: "text-only", verdict: VERDICT.refused },
    { provider: "opencode1", model: "sees-images", verdict: VERDICT.quota },
    { provider: "opencode1", model: "text-only", verdict: VERDICT.quota },
    { provider: "opencode1", model: "elsewhere-only", verdict: VERDICT.dead },
  ];
  const decisions = decisionsFrom(results, providers);
  // The capped account inherits from the same endpoint, trailing slash and all.
  assert.equal(decisions.get("opencode1/sees-images"), true);
  assert.equal(decisions.get("opencode1/text-only"), false);
  assert.equal(decisions.get("opencode2/sees-images"), true);
  // A different endpoint answers for itself or not at all: same model id, and
  // the reseller in front of it is what decides whether images get through.
  assert.equal(decisions.has("command-code/sees-images"), false);
  // Nothing conclusive was learned about this one.
  assert.equal(decisions.has("opencode1/elsewhere-only"), false);

  // Two direct answers that disagree leave the entry alone rather than letting
  // whichever ran last win.
  const conflicted = decisionsFrom(
    [
      { provider: "opencode1", model: "sees-images", verdict: VERDICT.vision },
      { provider: "opencode2", model: "sees-images", verdict: VERDICT.refused },
    ],
    providers,
  );
  assert.equal(conflicted.get("opencode1/sees-images"), true, "a direct verdict still stands for itself");
  assert.equal(conflicted.get("opencode2/sees-images"), false);
  assert.equal(conflicted.has("command-code/sees-images"), false);
}

const tests = [
  testParse,
  testApply,
  testClassifyAnswer,
  testClassifyError,
  testProbeVerdicts,
  testDecisionsFrom,
  testCredentials,
  testDshHome,
  testProbeImage,
];

let failed = 0;
for (const test of tests) {
  try {
    await test();
    console.log(`ok   ${test.name}`);
  } catch (err) {
    failed++;
    console.error(`FAIL ${test.name}: ${err?.message ?? err}`);
  }
}
if (failed > 0) {
  console.error(`${failed} test(s) failed`);
  process.exit(1);
}
console.log(`${tests.length} test(s) passed`);
