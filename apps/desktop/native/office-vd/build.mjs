#!/usr/bin/env node
// Builds the office virtual-screen tools into apps/desktop/resources/office-vd, which electron-builder
// ships unpacked inside the Mac app (asarUnpack resources/**).
//   node build.mjs [--arch arm64|x64|universal] [--out <dir>]
// Default arch: the host's. macOS only; on other platforms it does nothing.
import { chmodSync, copyFileSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const opt = (name, fallback) => { const i = argv.indexOf(name); return i >= 0 && argv[i + 1] ? argv[i + 1] : fallback; };
const output = resolve(opt('--out', resolve(root, '../../resources/office-vd')));
const archOpt = opt('--arch', process.arch === 'arm64' ? 'arm64' : 'x64');
const arches = { arm64: ['arm64'], x64: ['x86_64'], universal: ['arm64', 'x86_64'] }[archOpt];
if (!arches) throw new Error(`unknown --arch ${archOpt}`);

if (process.platform !== 'darwin') {
  console.log('office-vd: macOS only, skipping');
  process.exit(0);
}

function run(command, args) {
  const r = spawnSync(command, args, { cwd: root, stdio: 'inherit' });
  if (r.error) throw r.error;
  if (r.status !== 0) throw new Error(`${command} ${args.slice(0, 3).join(' ')} exited with ${r.status}`);
}

rmSync(output, { recursive: true, force: true });
mkdirSync(output, { recursive: true });

run('xcrun', [
  '--sdk', 'macosx', 'clang', '-fobjc-arc', '-O2',
  ...arches.flatMap(a => ['-arch', a]),
  '-mmacosx-version-min=12.3',
  '-framework', 'AppKit', '-framework', 'CoreGraphics', '-framework', 'Foundation',
  'src/vd-hold.m', 'src/VDVirtualDisplay.m',
  '-o', resolve(output, 'vd-hold'),
]);

const slices = arches.map(a => {
  const out = resolve(output, `screen-capture-${a}`);
  run('xcrun', [
    '--sdk', 'macosx', 'swiftc', '-O', '-swift-version', '5',
    '-target', `${a}-apple-macosx12.3`,
    'src/screen-capture.swift', '-o', out,
  ]);
  return out;
});
if (slices.length === 1) {
  copyFileSync(slices[0], resolve(output, 'screen-capture'));
} else {
  run('xcrun', ['lipo', '-create', ...slices, '-output', resolve(output, 'screen-capture')]);
}
for (const s of slices) rmSync(s);

for (const f of ['latch.mjs', 'broker.mjs', 'vd.mjs', 'LICENSE.yomado', 'README.md']) {
  copyFileSync(resolve(root, f), resolve(output, f));
}
chmodSync(resolve(output, 'vd-hold'), 0o755);
chmodSync(resolve(output, 'screen-capture'), 0o755);
console.log(`office-vd: built ${archOpt} into ${output}`);
