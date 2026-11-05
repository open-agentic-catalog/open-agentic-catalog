// Use the public API to keep preview foreground-only, including agent shells
// where the Astro CLI otherwise automatically starts a background daemon.
import path from 'node:path';
import { base, project } from './site.mjs';
import { servePreviewLicense } from './preview-download.mjs';

process.env.ASTRO_TELEMETRY_DISABLED = '1';
const { preview } = await import('astro');
const server = await preview({ server: { host: '127.0.0.1', port: 4321 } });
try {
  servePreviewLicense(server.server, path.join(project, 'dist'), base);
} catch (error) {
  await server.stop();
  throw error;
}
const stop = async () => { await server.stop(); };
process.once('SIGINT', stop);
process.once('SIGTERM', stop);
