import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { once } from 'node:events';
import { servePreviewLicense } from './preview-download.mjs';

test('preview serves only the exact built license at either base and retains Astro routing', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'catalog-docs-preview-'));
  const bytes = Buffer.from('Synthetic license\n');
  fs.mkdirSync(path.join(directory, 'generated'));
  fs.writeFileSync(path.join(directory, 'generated/LICENSE'), bytes);
  try {
    for (const prefix of ['/', '/team/catalog/']) {
      const server = http.createServer((_request, response) => {
        response.writeHead(404);
        response.end('Astro fallback');
      });
      servePreviewLicense(server, directory, prefix);
      server.listen(0, '127.0.0.1');
      await once(server, 'listening');
      const origin = `http://127.0.0.1:${server.address().port}`;
      try {
        for (const method of ['GET', 'HEAD']) {
          const response = await fetch(origin + prefix + 'generated/LICENSE', { method, signal: AbortSignal.timeout(5000) });
          assert.equal(response.status, 200);
          assert.equal(response.headers.get('content-length'), String(bytes.length));
          assert.equal(response.headers.get('x-content-type-options'), 'nosniff');
          assert.deepEqual(Buffer.from(await response.arrayBuffer()), method === 'HEAD' ? Buffer.alloc(0) : bytes);
        }
        for (const target of [prefix, prefix + 'generated/.env', prefix + 'generated/LICENSE/', '/wrong/generated/LICENSE']) {
          const response = await fetch(origin + target, { signal: AbortSignal.timeout(5000) });
          assert.equal(response.status, 404);
          assert.equal(await response.text(), 'Astro fallback');
        }
        const response = await fetch(origin + prefix + 'generated/LICENSE', { method: 'POST', signal: AbortSignal.timeout(5000) });
        assert.equal(response.status, 404);
      } finally {
        server.closeAllConnections();
        await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
      }
    }
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});
