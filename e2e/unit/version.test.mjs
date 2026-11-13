import { test } from 'node:test';
import assert from 'node:assert/strict';
import { versionAtLeast } from '../lib/version.mjs';

test('versionAtLeast compares dotted numeric versions, not strings', () => {
  assert.equal(versionAtLeast('0.10.1', '0.3.0'), true);
  assert.equal(versionAtLeast('0.3.0', '0.3.0'), true);
  assert.equal(versionAtLeast('v0.3', '0.3.0'), true);
  assert.equal(versionAtLeast('1.0.0', '0.10.1'), true);
  assert.equal(versionAtLeast('0.2.9', '0.3.0'), false);
  assert.equal(versionAtLeast('0.9.9', '0.10.1'), false);
  assert.equal(versionAtLeast('', '0.3.0'), false);
  assert.equal(versionAtLeast('dev', '0.3.0'), false);
  assert.equal(versionAtLeast(undefined, '0.3.0'), false);
});
