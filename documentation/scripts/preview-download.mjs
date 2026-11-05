// Astro's trailing-slash preview rule rejects the extensionless LICENSE file.
// Handle that one built download before passing every other request to Astro.
import fs from 'node:fs';
import path from 'node:path';
import { basePath } from './site.mjs';

export function servePreviewLicense(server, directory, prefix) {
  const target = basePath(prefix) + 'generated/LICENSE';
  const bytes = fs.readFileSync(path.join(directory, 'generated/LICENSE'));
  const listeners = server.listeners('request');
  if (listeners.length !== 1) throw new Error('Expected one Astro preview request handler');
  const original = listeners[0];
  server.removeListener('request', original);
  server.on('request', function (request, response) {
    if (request.url?.split('?')[0] !== target || !['GET', 'HEAD'].includes(request.method)) {
      return original.call(this, request, response);
    }
    response.writeHead(200, {
      'Content-Type': 'text/plain; charset=utf-8',
      'Content-Length': bytes.length,
      'X-Content-Type-Options': 'nosniff',
    });
    response.end(request.method === 'HEAD' ? undefined : bytes);
  });
}
