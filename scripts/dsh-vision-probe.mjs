#!/usr/bin/env node
// Probe which dsh models really accept image input, and declare it in
// ~/.dsh/settings.yaml.
//
// Why this exists (DENE-591): dsh gates its image tools on the model entry's
// `inputModalities`, which defaults to ["text"] when the field is absent. Every
// model entry a human hand-writes is therefore blind by default, no matter what
// the endpoint behind it supports — the tool refuses with `model "..." does not
// declare image input` and no request is ever sent. Multica does not own that
// file, so the fix cannot live in the daemon; it lives here, next to
// setup-dsh-runtime.sh, as a repeatable probe.
//
// Declaring the field by hand is not safe either. Three upstream behaviours are
// indistinguishable from the config side:
//
//   1. the endpoint answers about the image  -> declare "image"
//   2. the endpoint rejects the image part   -> must not declare (hard error)
//   3. the endpoint accepts the request and silently drops the image, then
//      answers from the text alone          -> must not declare, and this is
//      the dangerous one: the model invents an answer about a picture it never
//      saw, and nothing in the transcript says so.
//
// So the classification is empirical: send a generated two-colour PNG and ask
// which colours it holds. Only a model that names both, in order, is declared.
//
// Usage:
//   node scripts/dsh-vision-probe.mjs                 # probe, print a table
//   node scripts/dsh-vision-probe.mjs --apply         # also rewrite settings.yaml
//   node scripts/dsh-vision-probe.mjs --provider opencode2 --json
//
// Never prints credential values.

import { deflateSync } from "node:zlib";
import { copyFileSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { fileURLToPath } from "node:url";
import path from "node:path";

const IMAGE_MODALITY = "image";

// ---------------------------------------------------------------------------
// Test image
// ---------------------------------------------------------------------------

// A 64x64 PNG, top half red and bottom half blue, built here rather than
// committed: a binary fixture in the repo is one more thing to explain, and the
// probe needs an image no model can have memorised.
export function buildProbeImage() {
  const size = 64;
  const chunks = [];
  const chunk = (type, data) => {
    const out = Buffer.alloc(8 + data.length + 4);
    out.writeUInt32BE(data.length, 0);
    out.write(type, 4, "ascii");
    data.copy(out, 8);
    const crcInput = Buffer.concat([Buffer.from(type, "ascii"), data]);
    out.writeUInt32BE(crc32(crcInput), 8 + data.length);
    return out;
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // colour type: truecolour
  const raw = Buffer.alloc(size * (1 + size * 3));
  let offset = 0;
  for (let y = 0; y < size; y++) {
    raw[offset++] = 0; // filter: none
    for (let x = 0; x < size; x++) {
      const top = y < size / 2;
      raw[offset++] = top ? 0xff : 0x00;
      raw[offset++] = 0x00;
      raw[offset++] = top ? 0x00 : 0xff;
    }
  }
  chunks.push(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
  chunks.push(chunk("IHDR", ihdr));
  chunks.push(chunk("IDAT", deflateSync(raw)));
  chunks.push(chunk("IEND", Buffer.alloc(0)));
  return Buffer.concat(chunks);
}

let crcTable = null;
function crc32(buf) {
  if (!crcTable) {
    crcTable = new Int32Array(256);
    for (let n = 0; n < 256; n++) {
      let c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      crcTable[n] = c;
    }
  }
  let c = -1;
  for (let i = 0; i < buf.length; i++) c = crcTable[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
}

export const PROBE_PROMPT =
  "What two colors are in this image? Answer exactly as 'top=<color>, bottom=<color>'.";

// ---------------------------------------------------------------------------
// settings.yaml reading
// ---------------------------------------------------------------------------

export function dshHome(env = process.env, home = homedir()) {
  const override = (env.DSH_HOME ?? "").trim();
  return override === "" ? path.join(home, ".dsh") : override;
}

const indentOf = (line) => line.length - line.trimStart().length;

// Which key declares modalities depends on the plugin that owns the provider
// block, and getting this wrong is silent: schemastery drops keys a profile's
// schema does not define, so a wrong spelling neither errors nor takes effect —
// the model stays text-only and the image tool keeps refusing. `llm-pi-ai`
// (every OpenAI-compatible gateway route) spells it `input`; the native
// `llm-deepseek` catalog spells it `inputModalities`. DENE-591 was two rounds of
// the second key written into the first kind of block.
const MODALITY_KEYS = { "llm-pi-ai": "input", "llm-deepseek": "inputModalities" };
const KNOWN_KEYS = Object.values(MODALITY_KEYS);

export function modalityKeyFor(section) {
  return MODALITY_KEYS[section] ?? "inputModalities";
}

const declarationFor = (key) => `${key}: ["text", "image"]`;

// Line-based on purpose. A YAML round-trip would reformat a file this script
// does not own — dropping the user's comments, quoting and key order — and the
// only edit needed is one line per model entry.
export function parseProviders(text) {
  const lines = text.split("\n");
  const providers = [];
  let section = "";
  let providersIndent = -1;
  let providerIndent = -1;
  let current = null;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim() === "" || line.trimStart().startsWith("#")) continue;
    const indent = indentOf(line);
    if (indent === 0) {
      const top = line.trim().match(/^([A-Za-z0-9_.-]+):$/);
      section = top ? top[1] : "";
      providersIndent = -1;
      current = null;
      continue;
    }
    if (section === "") continue;
    if (providersIndent < 0) {
      if (/^providers:\s*$/.test(line.trim())) {
        providersIndent = indent;
        providerIndent = indent + 4;
      }
      continue;
    }
    if (indent <= providersIndent) {
      // Left the providers block, but still inside the same top-level section.
      providersIndent = -1;
      current = null;
      continue;
    }
    const providerMatch = indent === providerIndent && line.trim().match(/^([A-Za-z0-9_.-]+):$/);
    if (providerMatch) {
      current = {
        name: providerMatch[1],
        section,
        modalityKey: modalityKeyFor(section),
        line: i,
        apiKeyEnv: "",
        baseURL: "",
        headers: {},
        defaultInputImage: false,
        models: [],
      };
      providers.push(current);
      continue;
    }
    if (!current) continue;
    const scalar = line.trim().match(/^([A-Za-z0-9_.-]+):\s*(.*)$/);
    if (scalar && indent === providerIndent + 4) {
      const [, key, value] = scalar;
      if (key === "apiKeyEnv") current.apiKeyEnv = unquote(value);
      if (key === "baseURL") current.baseURL = unquote(value);
      // A provider-level default that already carries image changes what
      // "withdraw the declaration" has to mean for a blind model.
      if (key === "defaultInput") current.defaultInputImage = value.includes(IMAGE_MODALITY);
      continue;
    }
    const header = line.trim().match(/^([A-Za-z0-9_.-]+):\s*(.+)$/);
    if (header && indent === providerIndent + 8 && inSection(lines, i, "headers", providerIndent + 4)) {
      current.headers[header[1]] = unquote(header[2]);
      continue;
    }
    const model = line.trim().match(/^- id:\s*(.+)$/);
    if (model) {
      current.models.push({ id: unquote(model[1]), line: i, indent, declarations: [] });
      continue;
    }
    const field = line.trim().match(/^([A-Za-z0-9_.-]+):\s*(\[.*\]|.+)$/);
    if (field && KNOWN_KEYS.includes(field[1]) && current.models.length > 0) {
      const last = current.models[current.models.length - 1];
      if (indent === last.indent + 2) {
        last.declarations.push({ key: field[1], line: i, image: field[2].includes(IMAGE_MODALITY) });
      }
    }
  }
  for (const provider of providers) {
    for (const model of provider.models) {
      const effective = model.declarations.find((d) => d.key === provider.modalityKey);
      // A declaration under the other plugin's key is inert, so it is not what
      // the model currently claims — but it is still a line to clean up.
      model.declared = effective ? effective.image : provider.defaultInputImage;
      model.declaredLine = effective?.line;
      model.strayLines = model.declarations.filter((d) => d.key !== provider.modalityKey).map((d) => d.line);
    }
  }
  return providers;
}

// Walks back to the nearest key at the given indent, so a `name:` or
// `displayName:` two levels down is never read as a request header.
function inSection(lines, index, name, sectionIndent) {
  for (let i = index - 1; i >= 0; i--) {
    const line = lines[i];
    if (line.trim() === "") continue;
    const indent = indentOf(line);
    if (indent > sectionIndent) continue;
    if (indent < sectionIndent) return false;
    return line.trim() === `${name}:`;
  }
  return false;
}

function unquote(value) {
  const trimmed = value.trim();
  if (trimmed.length >= 2 && /^(".*"|'.*')$/.test(trimmed)) return trimmed.slice(1, -1);
  return trimmed;
}

// credentials refs are `NAME: value` under a `refs:` block; the value never
// leaves this process.
export function readCredentialRefs(text) {
  const refs = {};
  const lines = text.split("\n");
  const refsAt = lines.findIndex((l) => /^refs:\s*$/.test(l));
  if (refsAt < 0) return refs;
  for (let i = refsAt + 1; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim() === "") continue;
    if (indentOf(line) === 0) break;
    const match = line.trim().match(/^([A-Za-z0-9_.-]+):\s*(.+)$/);
    if (match) refs[match[1]] = unquote(match[2]);
  }
  return refs;
}

// ---------------------------------------------------------------------------
// Classification
// ---------------------------------------------------------------------------

export const VERDICT = {
  vision: "vision", // named both colours: safe to declare
  refused: "refused", // endpoint rejected the image part
  blind: "blind", // accepted the request, never saw the image
  dead: "dead", // model fails on plain text too
  quota: "quota", // out of quota; nothing learned
  unknown: "unknown",
};

export function classifyAnswer(content) {
  const text = (content ?? "").toLowerCase();
  const red = text.indexOf("red");
  const blue = text.indexOf("blue");
  if (red < 0 || blue < 0) return false;
  return red < blue;
}

export function classifyError(status, body) {
  const text = (body ?? "").toLowerCase();
  if (status === 429 || text.includes("usagelimit") || text.includes("rate_limit")) {
    return VERDICT.quota;
  }
  if (
    text.includes("image input") ||
    text.includes("image inputs") ||
    text.includes("unsupported content type") ||
    text.includes("unexpected item type in content")
  ) {
    return VERDICT.refused;
  }
  return VERDICT.unknown;
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

async function post(provider, apiKey, body, fetchImpl) {
  const url = `${provider.baseURL.replace(/\/$/, "")}/chat/completions`;
  const res = await fetchImpl(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${apiKey}`,
      // Zen's edge answers 403 (Cloudflare 1010) to a default Node agent.
      "User-Agent": "opencode/1.0",
      ...provider.headers,
    },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  return { status: res.status, text };
}

function contentOf(text) {
  try {
    const data = JSON.parse(text);
    const message = data?.choices?.[0]?.message?.content;
    if (typeof message === "string") return message;
    if (Array.isArray(message)) {
      return message.map((part) => (typeof part === "string" ? part : (part?.text ?? ""))).join(" ");
    }
    return "";
  } catch {
    return "";
  }
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// The Zen edge answers a plain 500 under back-to-back load, and a 500 read as a
// verdict is how a model that sees images gets recorded as one that does not.
// Retried before anything is concluded, because the probe's whole value is that
// its answers are trustworthy enough to write into a config file.
async function postWithRetry(provider, apiKey, body, fetchImpl, attempts, delayMs) {
  let last = null;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      last = await post(provider, apiKey, body, fetchImpl);
      if (last.status < 500) return last;
    } catch (err) {
      last = { status: 0, text: String(err?.message ?? err) };
    }
    if (attempt < attempts) await sleep(delayMs);
  }
  return last;
}

export async function probeModel({
  provider,
  model,
  apiKey,
  imageB64,
  fetchImpl,
  maxTokens = 2000,
  attempts = 3,
  retryDelayMs = 2000,
}) {
  const imageBody = {
    model,
    max_tokens: maxTokens,
    messages: [
      {
        role: "user",
        content: [
          { type: "text", text: PROBE_PROMPT },
          { type: "image_url", image_url: { url: `data:image/png;base64,${imageB64}` } },
        ],
      },
    ],
  };
  const res = await postWithRetry(provider, apiKey, imageBody, fetchImpl, attempts, retryDelayMs);
  if (res.status === 0) return { verdict: VERDICT.unknown, detail: firstLine(res.text) };
  if (res.status >= 400) {
    const verdict = classifyError(res.status, res.text);
    if (verdict === VERDICT.unknown) {
      // A 5xx says nothing about modality until plain text is ruled out: a
      // model that is simply down would otherwise be filed as text-only.
      const control = await postWithRetry(
        provider,
        apiKey,
        { model, max_tokens: 64, messages: [{ role: "user", content: "Reply with exactly: pong" }] },
        fetchImpl,
        attempts,
        retryDelayMs,
      );
      if (control.status >= 400 || control.status === 0) {
        return { verdict: VERDICT.dead, detail: firstLine(res.text) };
      }
    }
    return { verdict, detail: firstLine(res.text) };
  }
  const content = contentOf(res.text);
  if (classifyAnswer(content)) return { verdict: VERDICT.vision, detail: firstLine(content) };
  return { verdict: VERDICT.blind, detail: firstLine(content) };
}

const firstLine = (text) => (text ?? "").replace(/\s+/g, " ").trim().slice(0, 160);

// Image support is a property of the endpoint and model, not of the account, so
// a conclusive verdict carries to every other provider entry pointing at the
// same baseURL with the same model id. This is what keeps a second, quota-capped
// account from staying blind for a week: its models cannot be probed, but the
// question was already answered on the same endpoint.
export function decisionsFrom(results, providers) {
  const conclusive = new Map();
  const decisions = new Map();
  const endpointOf = new Map(
    providers.map((p) => [p.name, p.baseURL.replace(/\/$/, "")]),
  );
  for (const r of results) {
    if (r.verdict !== VERDICT.vision && r.verdict !== VERDICT.refused && r.verdict !== VERDICT.blind) {
      continue;
    }
    const want = r.verdict === VERDICT.vision;
    decisions.set(`${r.provider}/${r.model}`, want);
    const key = `${endpointOf.get(r.provider) ?? ""}|${r.model}`;
    // A direct answer from a second account beats an inherited one, and two
    // direct answers that disagree are not something to average: the safe
    // reading is "not proven", so the model keeps whatever the user wrote.
    const seen = conclusive.get(key);
    conclusive.set(key, seen === undefined || seen === want ? want : null);
  }
  for (const provider of providers) {
    const endpoint = provider.baseURL.replace(/\/$/, "");
    for (const model of provider.models) {
      const id = `${provider.name}/${model.id}`;
      if (decisions.has(id)) continue;
      const inherited = conclusive.get(`${endpoint}|${model.id}`);
      if (inherited === true || inherited === false) decisions.set(id, inherited);
    }
  }
  for (const [id, want] of decisions) {
    if (want === null) decisions.delete(id);
  }
  return decisions;
}

// ---------------------------------------------------------------------------
// settings.yaml editing
// ---------------------------------------------------------------------------

// Declares or withdraws image input for the named models, leaving every other
// byte of the file alone. Models whose probe learned nothing (quota, dead) are
// simply not passed in — an unproven model keeps whatever the user wrote.
export function applyModalities(text, decisions) {
  const lines = text.split("\n");
  const providers = parseProviders(text);
  const edits = [];
  for (const provider of providers) {
    for (const model of provider.models) {
      const want = decisions.get(`${provider.name}/${model.id}`);
      if (want === undefined) continue;
      // An inert declaration under the wrong key goes either way: it does
      // nothing today and would quietly start doing something if the provider
      // ever moved to the other plugin.
      for (const stray of model.strayLines) edits.push({ at: stray, remove: true });
      const declaration = `${" ".repeat(model.indent + 2)}${declarationFor(provider.modalityKey)}`;
      const textOnly = `${" ".repeat(model.indent + 2)}${provider.modalityKey}: ["text"]`;
      if (want) {
        if (model.declaredLine === undefined) edits.push({ at: model.line + 1, insert: declaration });
        else if (!model.declared) edits.push({ at: model.declaredLine, replace: declaration });
      } else if (provider.defaultInputImage) {
        // Deleting the line here would inherit image from the provider default,
        // which is the opposite of what the probe found.
        if (model.declaredLine === undefined) edits.push({ at: model.line + 1, insert: textOnly });
        else if (model.declared) edits.push({ at: model.declaredLine, replace: textOnly });
      } else if (model.declaredLine !== undefined && model.declared) {
        edits.push({ at: model.declaredLine, remove: true });
      }
    }
  }
  if (edits.length === 0) return { text, changed: 0 };
  edits.sort((a, b) => b.at - a.at);
  for (const edit of edits) {
    if (edit.remove) lines.splice(edit.at, 1);
    else if (edit.replace !== undefined) lines[edit.at] = edit.replace;
    else lines.splice(edit.at, 0, edit.insert);
  }
  return { text: lines.join("\n"), changed: edits.length };
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const opts = { apply: false, json: false, providers: [], models: [] };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--apply") opts.apply = true;
    else if (arg === "--json") opts.json = true;
    else if (arg === "--provider") opts.providers.push(argv[++i]);
    else if (arg === "--model") opts.models.push(argv[++i]);
    else if (arg === "-h" || arg === "--help") opts.help = true;
    else throw new Error(`Unknown option: ${arg}`);
  }
  return opts;
}

const HELP = `Usage: node scripts/dsh-vision-probe.mjs [options]

Probe every dsh model for real image support and report a verdict per model.

Options:
  --apply             Write inputModalities into settings.yaml (backup first)
  --provider NAME     Limit to a provider (repeatable)
  --model ID          Limit to a model id (repeatable)
  --json              Machine-readable output
  -h, --help          Show this help

Verdicts:
  vision   answered about the image      -> declared with --apply
  refused  endpoint rejected the image   -> declaration withdrawn with --apply
  blind    accepted it, never saw it     -> declaration withdrawn with --apply
  dead     fails on plain text too       -> left alone
  quota    out of quota                  -> left alone

A conclusive verdict also applies to any other provider entry with the same
baseURL and model id, so a second account that is capped or unprobed still gets
the declaration the endpoint already answered for.

Credential values are never printed.`;

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) {
    console.log(HELP);
    return;
  }
  const home = dshHome();
  const settingsPath = path.join(home, "settings.yaml");
  const settings = readFileSync(settingsPath, "utf8");
  const credentialsPath = path.join(home, ".credentials.yaml");
  let refs = {};
  try {
    refs = readCredentialRefs(readFileSync(credentialsPath, "utf8"));
  } catch {
    refs = {};
  }
  const imageB64 = buildProbeImage().toString("base64");
  const providers = parseProviders(settings).filter(
    (p) => opts.providers.length === 0 || opts.providers.includes(p.name),
  );
  if (providers.length === 0) throw new Error(`No providers found in ${settingsPath}`);

  const results = [];
  for (const provider of providers) {
    const apiKey = process.env[provider.apiKeyEnv] || refs[provider.apiKeyEnv] || "";
    if (apiKey === "") {
      console.error(`skip ${provider.name}: no value for ${provider.apiKeyEnv}`);
      continue;
    }
    for (const model of provider.models) {
      if (opts.models.length > 0 && !opts.models.includes(model.id)) continue;
      const outcome = await probeModel({
        provider,
        model: model.id,
        apiKey,
        imageB64,
        fetchImpl: fetch,
      });
      results.push({
        provider: provider.name,
        model: model.id,
        declared: model.declared === true,
        ...outcome,
      });
      if (!opts.json) {
        console.log(
          `${outcome.verdict.padEnd(8)} ${provider.name}/${model.id}${
            outcome.verdict === VERDICT.vision ? "" : `  — ${outcome.detail}`
          }`,
        );
      }
    }
  }

  const decisions = decisionsFrom(results, parseProviders(settings));

  if (opts.apply) {
    const { text, changed } = applyModalities(settings, decisions);
    if (changed === 0) {
      console.error("settings.yaml already matches the probe; nothing written.");
    } else {
      const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\..*/, "");
      const backup = `${settingsPath}.bak-vision-${stamp}`;
      copyFileSync(settingsPath, backup);
      writeFileSync(settingsPath, text);
      console.error(`Wrote ${changed} change(s) to ${settingsPath} (backup: ${path.basename(backup)})`);
      console.error("dsh resolves model capability at session start — start a new session to pick this up.");
    }
  }

  if (opts.json) {
    console.log(JSON.stringify({ settings: settingsPath, results }, null, 2));
  } else {
    const counts = results.reduce((acc, r) => ({ ...acc, [r.verdict]: (acc[r.verdict] ?? 0) + 1 }), {});
    console.log(
      `\n${results.length} model(s): ` +
        Object.entries(counts)
          .map(([k, v]) => `${k}=${v}`)
          .join(" "),
    );
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((err) => {
    console.error(`✗ ${err?.message ?? err}`);
    process.exit(1);
  });
}
