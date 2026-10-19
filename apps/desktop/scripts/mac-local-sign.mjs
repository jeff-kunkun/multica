// electron-builder `mac.sign` hook for local builds: signs with the fixed
// self-signed identity from scripts/mac-local-signing.sh so every build keeps
// the same designated requirement and macOS privacy grants (Full Disk Access)
// survive upgrades.
//
// electron-builder only auto-picks identities macOS trusts, and silently
// falls back to ad-hoc for a self-signed one. A custom sign hook skips that
// lookup; we then hand electron-builder's prepared options (entitlements,
// hardened runtime, ignore rules) to the same @electron/osx-sign it uses.
//
// package.mjs wires this up and passes the identity through
// MULTICA_MAC_SIGN_IDENTITY / MULTICA_MAC_SIGN_KEYCHAIN.

import { createRequire } from "node:module";

function loadOsxSign() {
  const require = createRequire(import.meta.url);
  const fromBuilder = createRequire(require.resolve("electron-builder"));
  const fromLib = createRequire(fromBuilder.resolve("app-builder-lib"));
  return fromLib("@electron/osx-sign");
}

export default async function sign(opts) {
  const identity = process.env.MULTICA_MAC_SIGN_IDENTITY;
  const keychain = process.env.MULTICA_MAC_SIGN_KEYCHAIN;
  if (!identity || !keychain) {
    throw new Error(
      "mac-local-sign: MULTICA_MAC_SIGN_IDENTITY / MULTICA_MAC_SIGN_KEYCHAIN not set; run through scripts/package.mjs",
    );
  }
  const { signAsync } = loadOsxSign();
  // A self-signed identity is never notarized, so it needs no secure
  // timestamp. Asking Apple's timestamp server once per file fails the whole
  // build whenever one of those hundreds of requests is dropped.
  const optionsForFile = opts.optionsForFile;
  await signAsync({
    ...opts,
    identity,
    keychain,
    identityValidation: false,
    optionsForFile: (filePath) => ({
      ...optionsForFile?.(filePath),
      timestamp: "none",
    }),
  });
}
