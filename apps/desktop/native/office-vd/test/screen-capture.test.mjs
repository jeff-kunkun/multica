// What the capture helper may show. Runs against the built binary (node build.mjs), via --decide,
// which needs no screen-recording permission. Skipped where the helper isn't built.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const bin = process.env.OFFICE_VD_SCREEN_CAPTURE
  || fileURLToPath(new URL('../../../resources/office-vd/screen-capture', import.meta.url));
const built = fs.existsSync(bin);

const vd = { bundle: '', layer: 0, x: 1920, y: 1080, width: 1920, height: 1080 };
const chrome = (x, y, w = 800, h = 600) => ({ bundle: 'com.google.Chrome', layer: 0, x, y, width: w, height: h });
const base = { displayFound: true, isMain: false, vendor: 505, model: 0x5644, serial: 0x56443201, frame: vd, windows: [], owned: 0, browser: 'com.google.Chrome' };

function decide(t) {
  const r = spawnSync(bin, ['--decide'], { input: JSON.stringify({ ...base, ...t }) });
  assert.equal(r.status, 0, r.stderr.toString());
  return JSON.parse(r.stdout.toString());
}

test('shows agent windows on the office display, never other apps', { skip: !built }, () => {
  const d = decide({
    windows: [chrome(1960, 1120), { ...chrome(2000, 1200), bundle: 'com.apple.Terminal' }, chrome(100, 100)],
    owned: 1,
  });
  assert.deepEqual([d.ok, d.reason, d.windowIndexes], [true, 'ok', [0]]);
});

test('more browser windows than the broker registered stops the picture', { skip: !built }, () => {
  const d = decide({ windows: [chrome(1960, 1120), chrome(2800, 1300)], owned: 1 });
  assert.deepEqual([d.ok, d.reason], [false, 'unowned_window']);
});

test('a window hanging over onto another screen stops the picture', { skip: !built }, () => {
  const d = decide({ windows: [chrome(1800, 1120)], owned: 5 });
  assert.deepEqual([d.ok, d.reason], [false, 'window_straddles_display']);
});

test('never captures the main display or a display vd-hold did not create', { skip: !built }, () => {
  assert.equal(decide({ isMain: true, windows: [chrome(1960, 1120)], owned: 1 }).reason, 'vd_is_main');
  assert.equal(decide({ vendor: 1552, model: 41005, windows: [chrome(1960, 1120)], owned: 1 }).reason, 'not_office_display');
  assert.equal(decide({ displayFound: false }).reason, 'vd_missing');
  // yomado's virtual displays use the same vendor and model (serial 0x594D0000 + n).
  assert.equal(decide({ serial: 0x594D0001, windows: [chrome(1960, 1120)], owned: 1 }).reason, 'not_office_display');
});

test('an empty office display is fine and shows nothing', { skip: !built }, () => {
  const d = decide({ windows: [chrome(100, 100)], owned: 0 });
  assert.deepEqual([d.ok, d.reason, d.windowIndexes], [true, 'no_windows', []]);
});

test('--check-permission only reports, it never prompts', { skip: !built }, () => {
  const r = spawnSync(bin, ['--check-permission'], { timeout: 5000 });
  assert.equal(r.status, 0);
  assert.equal(typeof JSON.parse(r.stdout.toString()).granted, 'boolean');
});
