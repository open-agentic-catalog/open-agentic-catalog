// Optional public edition. Ordinary UI/manual builds require no publication input.
import fs from 'node:fs';
import {fileURLToPath} from 'node:url';

export function edition(environment = process.env) {
  const site = environment.DOCS_SITE;
  if (!site) {
    if (environment.DOCS_EDITION_COMMIT) throw new Error('DOCS_SITE is required for an edition');
    return null;
  }
  const url = new URL(site);
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.pathname !== '/') throw new Error('DOCS_SITE must be an HTTPS origin');
  if (!/^[0-9a-f]{40}$/.test(environment.DOCS_EDITION_COMMIT ?? '')) throw new Error('DOCS_EDITION_COMMIT must be the full source commit');
  const version = fs.readFileSync(fileURLToPath(new URL('../../VERSION', import.meta.url)), 'utf8').trim();
  return {site: url.origin, version, commit: environment.DOCS_EDITION_COMMIT};
}
