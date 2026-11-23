import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { deliveryFixture } from './delivery.fixture.mjs';

test('docs-delivery refuses a changed manual and preserves files beside the requested edition', t => {
  const fixture = deliveryFixture(t);
  fs.appendFileSync(path.join(fixture.input, 'index.html'), '<p>Edited after the build.</p>');
  const result = fixture.run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Incomplete or changed manual; run make docs-build/);
  fixture.checkNeighbors(false);
});
