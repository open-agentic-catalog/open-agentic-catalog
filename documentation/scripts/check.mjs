// Build and inspect root/non-root sites for links, assets, API routes and diagrams.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { parse as html } from 'parse5';
import { parse as yaml } from 'yaml';
import { entries, project, root, walk, downloads, assertHandbookNavigation } from './site.mjs';
import { assertDiagram } from './diagrams.mjs';
import { assertIconAssets, assertNavigationIcons } from './navigation.mjs';
import { bundle, manifestName } from './bundle.mjs';

const iconDirectory = path.join(project, 'src/assets/navigation');
const icons = assertIconAssets(fs.readFileSync(path.join(project, 'src/styles/navigation.css'), 'utf8'),
  new Map(fs.readdirSync(iconDirectory).filter(name => name.endsWith('.svg')).map(name => [name, fs.readFileSync(path.join(iconDirectory, name), 'utf8')])));

// inspect validates the existing dist tree for one hosting base. It checks rendered
// output against source contracts rather than assuming a successful build is enough.
function inspect(base) {
  const dist = path.join(project,'dist');
  const files = walk(dist);
  const manifest = JSON.parse(fs.readFileSync(path.join(dist, manifestName), 'utf8'));
  assert.deepEqual(manifest, bundle(dist, base, fs.readFileSync(path.join(project, 'package-lock.json'))), 'Exact rendered bundle evidence');
  const generated = new Set([...downloads,'content-inventory.json','dependency-inventory.json','browser-inventory.json','third-party-notices.txt','favicon.svg'].map(f=>'generated/'+f));
  for (const filename of files) {
    const name = path.relative(dist,filename);
    assert(name === manifestName || name.endsWith('.html') || generated.has(name) || /^(?:_astro|pagefind)\//.test(name),`Unlisted artifact: ${name}`);
    assert(!/\.(?:node|so|dll|dylib|exe)$/.test(name),`Build-only binary shipped: ${name}`);
  }
  const pages = new Map();
  for (const filename of files.filter(f=>f.endsWith('.html'))) {
    const rendered = fs.readFileSync(filename,'utf8');
    assert(!rendered.includes('implementation-status.md') && !rendered.includes('reference/implementation-status/'),`Developer status leaked into site: ${filename}`);
    assert(!rendered.includes('Edit that source, not the generated page.') && !rendered.includes('(repository-only reference)'),`Build notice leaked into reader page: ${filename}`);
    const document = html(rendered);
    const ids = new Set(), links = [], resources = [], navigation = [], navigationLinks = [];
    let headings = 0, diagrams = 0;
    const asides = [], steps = [], code = [], copies = [], trees = [], tabs = [];
    const visit = (node, inNavigation = false, inContent = false) => {
      const attrs = Object.fromEntries((node.attrs??[]).map(a=>[a.name,a.value]));
      inNavigation ||= node.tagName === 'nav' && attrs['aria-label'] === 'Main';
      inContent ||= attrs.class?.split(/\s+/).includes('sl-markdown-content');
      if (inContent && (['table', 'pre'].includes(node.tagName) || attrs.class?.split(/\s+/).includes('sl-openapi-operation-method-path'))) {
        assert(attrs.tabindex !== undefined && Number(attrs.tabindex) >= 0, `Keyboard-accessible content scroller: ${filename}`);
      }
      if (attrs.id) { assert(!ids.has(attrs.id),`Duplicate HTML ID ${filename}#${attrs.id}`); ids.add(attrs.id); }
      if (node.tagName === 'h1') headings++;
      if (node.tagName === 'aside' && attrs.class?.split(' ').includes('starlight-aside')) asides.push(node);
      if (node.tagName === 'ol' && attrs.class?.split(' ').includes('sl-steps')) steps.push(node);
      if (node.tagName === 'starlight-file-tree') trees.push(node);
      if (node.tagName === 'starlight-tabs') tabs.push(node);
      if (node.tagName === 'pre') code.push(node);
      if (node.tagName === 'button' && attrs['data-code'] !== undefined) copies.push(attrs['data-code'].replaceAll('\u007f', '\n'));
      if (node.tagName === 'svg' && attrs.role === 'img') { assertDiagram(node); diagrams++; }
      if (attrs.href && node.tagName === 'a') links.push(attrs.href);
      if (attrs.href && node.tagName === 'a' && inNavigation) { navigation.push(attrs.href); navigationLinks.push(node); }
      if (attrs.src) resources.push(attrs.src);
      if (node.tagName === 'link' && ['stylesheet','modulepreload','icon','preload'].includes(attrs.rel)) resources.push(attrs.href);
      node.childNodes?.forEach(child => visit(child, inNavigation, inContent));
    };
    visit(document);
    assert.equal(headings,1,`Exactly one page title: ${filename}`);
    pages.set(filename,{ids,links,resources,navigation,navigationLinks,diagrams,asides,steps,code,copies,trees,tabs,document});
  }
  for (const [filename, page] of pages) {
    const current = new URL(base + path.relative(dist,filename).replace(/index\.html$/,''),'http://documentation.invalid');
    for (const [target,resource] of [...page.links.map(l=>[l,false]),...page.resources.map(l=>[l,true])]) {
      const url = new URL(target,current);
      if (url.origin !== current.origin) { assert(!resource,`Third-party dependency: ${filename}: ${target}`); continue; }
      assert(url.pathname.startsWith(base),`Escaped base ${base}: ${filename}: ${target}`);
      let resolved = path.join(dist,decodeURIComponent(url.pathname.slice(base.length)));
      if (fs.existsSync(resolved) && fs.statSync(resolved).isDirectory()) resolved = path.join(resolved,'index.html');
      assert(fs.existsSync(resolved),`Missing local target: ${filename}: ${target}`);
      if (url.hash && pages.has(resolved)) assert(pages.get(resolved).ids.has(decodeURIComponent(url.hash.slice(1))),`Missing anchor: ${filename}: ${target}`);
    }
  }
  for (const entry of entries) {
    const page = pages.get(path.join(dist,entry.route,'index.html'));
    assert(page,`Unbuilt page ${entry.source}`);
    assertHandbookNavigation(page.navigation,entries,base);
    assertNavigationIcons(page.navigationLinks,entries,base,icons);
  }
  const diagrams = {'': 2, 'contributing/testing-map': 1, 'reference/standards': 0, 'guides/agent-plugins': 1, 'architecture/overview': 1, 'architecture/deployment': 4, 'operations/deployment': 1, 'architecture/components': 2, 'architecture/domain': 1, 'architecture/publication': 1, 'architecture/federation': 1, 'architecture/security': 2, 'architecture/operations': 1, 'architecture/catalog-ui': 1, 'architecture/design': 2, 'operations/backup-restore': 1, 'operations/observability': 3, 'guides/integrating': 2, 'reference/configuration': 0, 'guides/access-management': 1, 'guides/publishing': 2, 'operations/federation': 1, 'operations/hybrid-search': 2, 'guides/catalog-ui': 0, 'operations/maintenance': 1, 'operations/webhooks': 1, 'operations/idempotency-repair': 1, 'guides/local-development': 0, 'guides/sample-catalog': 0, 'guides/two-catalog-federation': 1, 'guides/feedback': 1, 'guides/resource-authoring': 1, 'guides/mcp-server': 1, 'guides/contextforge': 1, 'operations/helm-monitoring': 1, 'operations/database-roles': 1};
  for (const [route, count] of Object.entries(diagrams)) {
    assert.equal(pages.get(path.join(dist,route,'index.html'))?.diagrams,count,`Documentation diagram count: ${route}`);
  }
  // Check selected content components. Keep required instructions visible
  // and compare copyable blocks with source, including nested Steps commands.
  const text = node => node.nodeName === '#text' ? node.value : (node.childNodes ?? []).map(text).join('');
  const attr = (node, name) => node.attrs?.find(a => a.name === name)?.value;
  const elements = (node, tag) => [...(node.tagName === tag ? [node] : []), ...(node.childNodes ?? []).flatMap(child => elements(child, tag))];
  // Assert links in page content, not only the sidebar. Unregistered README
  // references otherwise degrade to plain text while the build still succeeds.
  for (const [route, targets, phrases] of [
    ['operations/security', ['operations/database-roles/#identity-realm-administration'], ['These discovery checks do not apply when another active realm remains.', 'Publication activation and rollback require at least one active realm']],
    ['operations/docker', [], ['Remove or replace the development CATALOG_PUBLIC_BASE_URL in $CATALOG_INPUTS/runtime.env.', 'catalog_owner config validate --config /config/config.json --role migration']],
    ...['architecture/catalog-ui', 'reference/api-conventions'].map(route => [route, ['architecture/persistence/#000046---publisher-search-ordering'], ['PostgreSQL Publisher inserts, updates and deletes invalidate tenant Search generations']]),
    ['operations/troubleshooting', ['guides/search-cookbook/#origin-and-local-namespace-filters'], ['The Resource and its Listing share that namespace.', 'A Resource UUID or the textual namespace segment in an AIR identifier is not a substitute.']],
    ['guides/performance-and-monitoring', [], ['42 local Search/Explore probes, 24 indexed Search probes and 20 Overview probes, totaling 86.', 'eight publisher-order page probes']],
    ['architecture/decisions/0021-logging-observability-and-audit', [], ['GET and implicit HEAD for /api/v1/service-activity.']],
    ...['sdk/rust', 'contributing/testing'].map(route => [route, ['reference/dependencies/#rust-sdk-dependencies'], ['An audit result applies only to its selected inputs and advisory snapshot.', 'dependencyAudit: not_run', 'advisory data older than seven days']]),
    ...['sdk/rust', 'guides/rust-catalog-report'].map(route => [route, ['contributing/testing/#rust-sdk-verification'], ['separate audit procedure']]),
    ['guides/rust-catalog-report', ['reference/dependencies/#rust-sdk-dependencies'], ['Results depend on the selected installed inputs and advisory data no older than seven days.', 'Normal builds and tests do not establish audit success.']],
    ['operations/sign-in', ['#change-an-established-issuer-or-subject', 'operations/database-roles/#initial-catalog-identity-provisioning', 'operations/database-roles/#identity-realm-administration', 'guides/access-management/', 'reference/commands/#human-cli-login'], ['Matching emails or display names do not transfer grants, group membership or private saved items.', 'For a subject-policy change, restore the prior provider mapping before using the old membership.', 'A configuration rollback does not recreate revoked grants or offboarded memberships.']],
    ['operations/maintenance', ['#change-discovery-hosts-and-audiences', 'guides/publishing/#build-and-activate-discovery-separately', 'guides/publishing/#change-release-disposition-or-withdraw-discovery', 'operations/deployment/#verify-discovery-through-ingress-and-caches'], ['Changing authenticated to anonymous does not make the retained authenticated bundle public.', 'Removing a binding does not retire or delete retained snapshots.', 'Prepare and replace a host binding', 'Recover a failed host change']],
    ...['reference/configuration', 'guides/publishing'].map(route => [route, ['operations/maintenance/#change-discovery-hosts-and-audiences'], []]),
    ['operations/sign-in', ['operations/oidc-ibm-security-verify/'], []],
    ['reference/configuration', ['operations/oidc-ibm-security-verify/'], []],
    ['operations/oidc-ibm-security-verify', ['operations/sign-in/#deliver-the-configuration', 'operations/sign-in/#prepare-ui-secret-files', 'reference/ui-host-configuration/#host-configuration', 'reference/configuration/#oidc-private-trust', 'operations/database-roles/#initial-catalog-identity-provisioning', 'guides/access-management/', 'reference/commands/#human-cli-login', 'operations/maintenance/#rotate-credentials-and-trust', 'operations/oidc-keycloak/#run-the-local-qualification'], ['Hosted IBM Verify login has not been tested by this project.', 'Public client (no client secret)', 'https://browse.example.com/catalog/auth/callback', 'http://127.0.0.1:49152/oauth/callback', 'personal.write', 'Verify your deployment']],
    ['guides/concepts', ['guides/search-cookbook/#restrict-the-same-query-to-one-namespace', 'guides/search-cookbook/#save-this-group-as-a-private-named-search', 'guides/catalog-ui/#save-resources-and-searches', 'guides/resource-authoring/#describe-listing-relationships', 'guides/agent-plugins/', 'operations/federation/#adopt-one-observation-as-a-local-draft', 'guides/draft-transfer/', 'guides/contextforge/#discover-and-use-a-published-virtual-server'], ['Organize and reuse resources', 'Canonical Resource identifiers are unique across the tenant.', 'Shared curated collections and curated subsets for individual discovery hosts are unavailable.']],
    ['guides/search-cookbook', ['guides/catalog-ui/#save-resources-and-searches', 'guides/concepts/#organize-and-reuse-resources'], ['Save this group as a private named search', 'library.own', 'personal.write', 'Sharing a page URL transfers neither the saved search nor access to its targets.']],
    ['guides/vscode', ['guides/two-catalog-federation/', 'operations/oidc-keycloak/#connect-the-desktop-client', 'contributing/build-and-release/#optional-vs-code-package', '#first-use-and-command-index', '#manage-my-tools', '#agent-outcomes', '#browse-the-catalog', '#sign-in-and-sign-out', '#trust-proxies-and-limits', '#add-a-connection', '#combine-filters', '#personal-ratings-and-private-feedback', '#private-package-credentials', '#platform-and-remote-setup', '#remember-an-mcp-reference'], ['Catalog: Help (Offline)', 'Walk through four resource kinds', 'Catalog: Recover Opinion Command', 'Catalog: Manage Package Source Credentials']],
    ['sdk/overview', ['sdk/go/', 'sdk/python/', 'sdk/typescript/', 'sdk/rust/', 'sdk/rust/#export-a-reusable-draft'], ['Choose a language', 'no SDK package has been published', 'CATALOG_SDK_ORIGIN', 'Rust supports typed reusable-draft export through Client::export_version_revision_draft']],
    ['sdk/go', ['sdk/overview/', 'guides/integrating/#search-one-page-with-go'], ['go mod edit -replace=', 'ResponseBytes', 'go run .', 'Catalog API connection succeeded']],
    ['sdk/python', ['sdk/overview/', 'guides/integrating/#search-one-page-with-python'], ['python -m pip install ./sdk/python', 'CatalogSearchResult', 'python catalog_example.py', 'Find an operation']],
    ['sdk/typescript', ['sdk/overview/', 'guides/integrating/#search-one-page-with-javascript-or-typescript'], ['npm install /absolute/path/', 'createCatalogClient', 'node catalog-example.mjs', 'Catalog API connection succeeded']],
    ['sdk/rust', ['sdk/overview/#choose-a-language', 'guides/integrating/#use-the-rust-client'], ['open-agentic-catalog', 'Rust 1.88', 'Client::raw', 'Whole call timeout', 'Create a resource', 'Read exact bytes', 'Find an operation', 'Troubleshoot a call']],
    ['guides/integrating', ['sdk/overview/', 'sdk/go/', 'sdk/python/', 'sdk/typescript/', 'sdk/rust/', 'sdk/rust/#create-a-resource', 'sdk/rust/#read-exact-bytes', 'sdk/rust/#find-an-operation', 'guides/mcp-server/'], []],
    ['guides/langgraph-agent', ['sdk/python/', 'guides/sample-catalog/#start-the-catalog-with-compose-and-load-sample-data', 'contributing/testing/#optional-langgraph-discovery-example'], ['make langgraph-tools-install', 'make test-langgraph-model', 'CATALOG_SDK_CA_FILE', 'search_catalog', 'read_catalog_record']],
    ['guides/rust-catalog-report', ['sdk/rust/', 'guides/reader-quickstart/', 'guides/sample-catalog/#start-the-catalog-with-compose-and-load-sample-data'], ['cargo fetch --locked', '--token-file', '--allow-loopback-http', 'Web Application Testing', 'No authorized matches.', 'Incomplete', 'make sdk-check SDK_LANGUAGE=rust']],
    ['guides/assistant-discovery', ['guides/mcp-server/'], []],
    ['guides/mcp-server', ['sdk/overview/', 'guides/assistant-discovery/', 'guides/local-development/#start-the-service', 'guides/access-management/', 'reference/commands/#export-a-native-document', 'contributing/testing/#catalog-control-tools-over-stdio'], ['mcpServers', 'catalogctl mcp serve', 'catalog_control_capabilities', 'catalog_control_search', 'catalog_control_release', 'catalog_control_indexed_record', 'catalog_control_explore', 'catalog_control_access', 'CATALOG_REMOTE_TOKEN_FILE', '--max-tool-bytes', 'test-interoperability-mcp-catalog']],
    ['reference/commands', ['guides/mcp-server/', 'reference/configuration/#ans-registration-verification', 'operations/security/#ans-verification-authorities', 'operations/federation/#request-an-ans-registration-assessment'], []],
    ['reference/standards', ['guides/mcp-server/'], []],
    ['architecture/components', ['sdk/overview/'], []],
    ['guides/reader-quickstart', ['guides/search-cookbook/', 'guides/understand-results/'], ['My access']],
    ['guides/search-cookbook', ['guides/sample-catalog/', 'guides/understand-results/#read-a-local-result', 'guides/understand-results/#read-an-external-result', 'guides/two-catalog-federation/'], ['Web Application Testing', 'Everything', 'Web Testing Toolkit', 'A2A Hello World']],
    ['guides/understand-results', ['guides/search-cookbook/', '#copy-references-and-download-documents'], ['versionPresent', 'presentationSourceId']],
    ['guides/access-management', ['#development-identities-and-role-handoffs', 'reference/configuration/#manage-settings-in-the-ui'], []],
    ['guides/resource-authoring', ['guides/understand-results/#resource-artwork'], []],
    ['guides/catalog-ui', ['guides/publishing/#publish-an-agent-in-the-browser', 'operations/federation/#continue-from-the-two-catalog-tutorial'], []],
    ['guides/two-catalog-federation', ['operations/federation/#continue-from-the-two-catalog-tutorial'], []],
    ['guides/integrating', ['guides/understand-results/#copy-references-and-download-documents', 'guides/resource-authoring/#load-a-local-json-or-markdown-document'], []],
    ['operations/federation', ['#request-an-ans-assessment-in-the-ui', '#read-a-private-ans-assessment', 'guides/publishing/#review-the-browser-submission', 'guides/publishing/#release-and-publish-the-browser-agent', 'generated/documentation/assets/catalog-ui/source-confirmation.png'], ['Troubleshoot source tests, commands and retained evidence', 'It cannot recall a peer request already sent.']],
    ['operations/observability', ['#inspect-service-information-in-the-ui', 'generated/documentation/assets/catalog-ui/jobs-filtered.png'], ['Investigate one retained Job', 'There is no raw-result JSON fallback.']],
    ['operations/hybrid-search', ['operations/observability/#investigate-one-retained-job', 'generated/documentation/assets/catalog-ui/search-indexing-confirmation.png'], ['Check a rebuild outcome', 'Accounting includes reused vectors and failed inputs']],
    ['operations/troubleshooting', ['guides/search-cookbook/#origin-and-local-namespace-filters'], []],
    ['reference/api-assets', ['generated/api/examples/hybrid-search.json'], []],
    ['reference/profiles', ['reference/standards/#verify-and-update-standards-profiles', 'contributing/testing/#schema-validation-and-ard-conformance'], []],
    ['reference/helm', ['operations/helm-monitoring/'], []],
    ['reference/receiver-example', ['generated/LICENSE'], ['examples/webhookreceiver/verify.go']],
    ...['operations/docker', 'operations/azure-container-apps', 'operations/ibm-code-engine', 'operations/google-cloud-run', 'operations/sign-in'].map(route => [route, ['reference/ui-host-configuration/#host-configuration'], []]),

  ]) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const content = elements(page.document, 'div').find(node => attr(node, 'class')?.split(' ').includes('sl-markdown-content'));
    assert(content, `Readable manual content: ${route}`);
    const links = elements(content, 'a').map(node => attr(node, 'href'));
    for (const target of targets) {
      const href = target.startsWith('#') ? target : base + target;
      assert(links.includes(href), `Clickable manual content link: ${route}: ${target}`);
    }
    for (const phrase of phrases) assert(text(content).includes(phrase), `Manual guide content: ${route}: ${phrase}`);
  }
  // Replacing an SVG must preserve the decisions in visible, searchable content.
  const replacements = [
    ['architecture/design', ['Capability', 'Availability'], ['Hybrid search', 'ANS registration checks', 'Managed media']],
    ['architecture/catalog-ui', ['Source', 'Responsibility'], ['src/', 'cmd/catalog-ui/', 'internal/']],
    ['guides/publishing', ['Correction', 'New version label?', 'Supported next step'], ['Unsealed native content', 'Sealed native content']],
    ['reference/standards', ['Standard', 'What it does', 'How Open Agentic Catalog uses it'], ['Agentic Resource Discovery', 'Model Context Protocol']],
    ['architecture/domain', ['Boundary', 'Authenticated Control Search', 'Host-bound ARD auto Search'], ['Durable input', 'Continuation', 'Replay failure']],
    ['operations/federation', ['Caller federation value', 'Contacts upstream Search', 'Returns referral descriptors'], ['none', 'referrals', 'auto']],
    ['guides/integrating', ['Command outcome', 'Next action'], ['Known success', 'Unknown outcome', 'Stale revision']],
    ['guides/sample-catalog', ['Receipt state', 'Next action', 'Completion check'], ['Matching completed receipt', 'Explicit extension', 'Interrupted or uncertain command']],
    ['operations/maintenance', ['Stage', 'Key ring', 'Writer', 'Readers', 'Before the next stage'], ['Add the new pair', 'Keep the overlap', 'Retire the old pair']],
    ['reference/configuration', ['Entry point', 'Resolution and ownership'], ['Direct backend binary', 'Independent UI']],
  ];
  for (const [route, headers, terms] of replacements) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const content = elements(page.document, 'div').find(node => attr(node, 'class')?.split(' ').includes('sl-markdown-content'));
    const tables = elements(content, 'table').filter(table => {
      const actual = elements(table, 'th').map(node => text(node).trim().replace(/\s+/g, ' '));
      return headers.every(header => actual.includes(header));
    });
    assert.equal(tables.length, 1, `Diagram replacement table: ${route}`);
    for (const term of terms) assert(text(tables[0]).includes(term), `Preserved diagram decision: ${route}: ${term}`);
  }
  const uiArchitecture = pages.get(path.join(dist, 'architecture/catalog-ui', 'index.html'));
  assert.equal(uiArchitecture.trees.length, 1, 'UI source tree replaces its directory SVG');
  const uiSource = fs.readFileSync(path.join(project, 'src/content/docs/architecture/catalog-ui.mdx'), 'utf8').replace(/<FileTree>[\s\S]*?<\/FileTree>/g, '');
  for (const filename of ['catalog-ui/src/', 'catalog-ui/cmd/catalog-ui/', 'catalog-ui/internal/']) {
    assert(uiSource.includes(filename), `Searchable UI source path outside FileTree: ${filename}`);
  }
  const callouts = [
    ['guides/resource-authoring', 'Retention is not approval', 'note'],
    ['guides/publishing', 'Use a disposable Catalog', 'caution'],
    ['operations/backup-restore', 'Keep the restore isolated', 'caution'],
    ['operations/sign-in', 'Login does not grant access', 'note'],
    ['operations/federation', 'A failed sync is not an empty catalog', 'note'],
    ['operations/docker', 'Docker and Make use different file syntax', 'note'],
    ['guides/integrating', 'A timeout can hide a committed command', 'caution'],
    ['operations/hybrid-search', 'Permitted fields can contain sensitive text', 'note'],
    ['operations/incident-response', 'Smoke changes Catalog state', 'caution'],
  ];
  for (const [route, title, type] of callouts) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const asides = page.asides.filter(node => attr(node, 'aria-label') === title);
    assert.equal(asides.length, 1, `Labelled aside: ${route}`);
    assert(attr(asides[0], 'class').split(' ').includes(`starlight-aside--${type}`));
    const entry = entries.find(e => e.route === route);
    const source = fs.readFileSync(path.join(root, entry.source), 'utf8');
    const content = source.match(new RegExp(String.raw`:::${type}\[${title}\]\n([\s\S]*?)\n:::`))?.[1];
    assert(content, `Source callout: ${route}`);
    // Markdown uses typographic apostrophes in prose. Code comparisons below
    // remain byte-exact and must not receive this normalization.
    assert(text(asides[0]).replaceAll('\u2019', "'").includes(content), `Callout text: ${route}`);
  }
  const overview = pages.get(path.join(dist, 'architecture/overview', 'index.html'));
  const grids = elements(overview.document, 'div').filter(node => attr(node, 'class')?.split(' ').includes('card-grid'));
  assert.equal(grids.length, 1, 'One architecture navigation grid');
  assert(!attr(grids[0], 'class').split(' ').includes('stagger'), 'Architecture links retain reading order');
  const cards = elements(grids[0], 'div').filter(node => attr(node, 'class')?.split(' ').includes('sl-link-card'));
  const destinations = [
    ['Logical applications and data', 'architecture/deployment/#logical-applications-and-data', 'See the browser client, UI host, API, worker and Catalog data store.'],
    ['Backend layers and components', 'architecture/components/', 'See source dependencies, domain ports and runtime calls.'],
    ['Compose environment', 'architecture/deployment/#docker-compose', 'See the local containers, loopback ports and startup order.'],
    ['Helm environment', 'architecture/deployment/#kubernetes-through-helm', 'See chart workloads, optional routing and external infrastructure.'],
    ['Governed lifecycle', 'architecture/domain/', 'Follow a resource from authoring through review, release, and discovery.'],
    ['Federation process', 'architecture/federation/', 'Follow an administrator-approved source from inspection to bounded search.'],
    ['Standards and interoperability', 'reference/standards/', 'Understand pinned formats, local governance and external observations.'],
    ['Architecture decisions', 'architecture/decisions/', 'Review the rationale, alternatives and consequences behind these boundaries.'],
  ];
  assert.equal(cards.length, destinations.length, 'All architecture destinations');
  destinations.forEach(([title, destination, description], index) => {
    const links = elements(cards[index], 'a');
    assert.equal(links.length, 1, `One task per architecture card: ${title}`);
    assert.equal(text(links[0]).trim(), title, 'Preserved architecture link name');
    assert.equal(attr(links[0], 'href'), base + destination, 'Base-aware architecture destination');
    assert(elements(cards[index], 'span').some(node => attr(node, 'class')?.split(' ').includes('description') && text(node) === description), 'Preserved architecture description');
  });
  const statusCases = [
    ['reference/standards', 'Experimental', ['mcp-server-card-v1-experimental-526201b', 'mcp_server', 'with the experimental format identified']],
    ['reference/configuration', 'Unavailable', ['managedMedia.enabled', 'softwareAssessment.enabled', 'fails before runtime construction', 'Declaring a provider cannot implement either capability']],
  ];
  for (const [route, label, explanations] of statusCases) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const badges = elements(page.document, 'span').filter(node => attr(node, 'class')?.split(' ').includes('sl-badge') && text(node) === label);
    assert.equal(badges.length, 1, `One concrete status: ${route}`);
    assert(attr(badges[0], 'class').split(' ').includes('caution'), 'Status is not a success or trust claim');
    let row = badges[0].parentNode;
    while (row && row.tagName !== 'tr') row = row.parentNode;
    assert(row, `Status remains in its contract row: ${route}`);
    for (const phrase of explanations) assert(text(row).includes(phrase), `Status explanation: ${route}: ${phrase}`);
  }
  const mainRequirements = [
    ['operations/docker', 'Do not pass the repository\'s Make `.env` wholesale.'],
    ['guides/integrating', 'replay still requires current authentication, authorization and disclosure permission'],
    ['guides/integrating', 'reconcile authorized target state before deciding whether to submit another mutation'],
    ['operations/hybrid-search', 'Review the provider\'s hosting, retention and training policies before enabling it.'],
    ['operations/incident-response', 'Any mutating validation against shared or production data requires explicit operator authorization'],
    ['reference/configuration', 'The fragment is not a complete deployment configuration'],
    ['reference/configuration', 'These settings do not enable the optional UI or supply its required identity, session, URL or trust inputs'],
    ['operations/search-projection-repair', 'Drain API and worker processes before this upgrade'],
    ['reference/commands', 'It does not populate a separately running Catalog'],
    ['reference/commands', 'The UI probe uses a fixed port, ignores `--config` and does not test API or issuer availability'],
    ['architecture/persistence', 'connects to the selected PostgreSQL restore for bounded logical-integrity checks'],
  ];
  for (const [route, phrase] of mainRequirements) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    assert(elements(page.document, 'p').some(node => {
      if (!text(node).replaceAll('\u2019', "'").includes(phrase.replaceAll('`', ''))) return false;
      for (let parent = node.parentNode; parent; parent = parent.parentNode) {
        if (['aside', 'details', 'starlight-tabs'].includes(parent.tagName)) return false;
      }
      return true;
    }), `Required instruction stays in main text: ${route}: ${phrase}`);
  }
  for (const route of new Set([
    ...callouts.map(([route]) => route),
    'sdk/overview', 'sdk/go', 'sdk/python', 'sdk/typescript', 'sdk/rust',
    'guides/agent-plugins', 'guides/local-development', 'guides/sample-catalog',
    'guides/two-catalog-federation', 'guides/langgraph-agent',
    'contributing/documentation', 'contributing/make-targets',
    'operations/docker', 'operations/database-roles', 'operations/oidc-keycloak',
    'reference/configuration', 'operations/search-projection-repair', 'reference/commands',
  ])) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const entry = entries.find(e => e.route === route);
    const source = fs.readFileSync(path.join(root, entry.source), 'utf8');
    const blocks = [...source.matchAll(/^( *)(?:```)[^\n]*\n([\s\S]*?)^\1```/gm)].map(([, indent, body]) =>
      body.replace(new RegExp(`^${indent}`, 'gm'), '').trimEnd());
    const rendered = page.code.map(node => {
      // Expressive Code uses one .ec-line element per logical code line.
      const lines = [];
      const collect = child => {
        if (attr(child, 'class')?.split(' ').includes('ec-line')) {
          // Empty lines have a newline sentinel for layout, not a second line.
          const line = text(child);
          lines.push(line === '\n' ? '' : line);
        }
        else child.childNodes?.forEach(collect);
      };
      collect(node);
      return lines.join('\n');
    });
    assert.deepEqual(rendered, blocks, `Code content: ${route}`);
    assert.deepEqual(page.copies, blocks, `Clipboard content: ${route}`);
  }
  const commands = pages.get(path.join(dist, 'reference/commands', 'index.html'));
  const commandHeadings = elements(commands.document, 'h2').map(node => attr(node, 'id'));
  for (const id of ['obtain-and-invoke-the-binaries', 'process-commands', 'help-output-and-exit-status', 'remote-catalog-reads', 'operator-commands']) {
    assert(commandHeadings.includes(id), `CLI section remains accessible: ${id}`);
    assert(!commandHeadings.includes('api-development-checks'), 'Make procedures have their own contributor page');
  }
  assert(commands.ids.has('optional-ui-host'), 'The independent UI host has a direct reference anchor');
  const makeTargets = pages.get(path.join(dist, 'contributing/make-targets', 'index.html'));
  for (const id of ['maintained-make-workflows', 'api-development-checks', 'frontend-assertion-strength-and-benchmark-comparison']) {
    assert(commands.ids.has(id), `Forwarding command anchor: ${id}`);
    assert(makeTargets.ids.has(id), `Moved Make procedure anchor: ${id}`);
  }
  const tabCases = [
    ['guides/local-development', [
      [['Local (memory)', 'Docker (memory)', 'Docker Compose', 'Kubernetes (Helm)'], ['make run', '"$CATALOG_IMAGE" all --config /config/config.json', 'make compose-up LOCAL_UI=on', 'make helm-deploy-check']],
    ], ['start-the-service', 'stop-and-clean-up-local-runs'],
    ['Stop and reset have different effects', 'Run smoke only against a disposable Catalog']],
    ['guides/sample-catalog', [
      [['One command', 'Step by step'], [[
        'make demo-up \\',
        '  CATALOG_ENV_FILE="$CATALOG_ENV_FILE" \\',
        '  COMPOSE_PROJECT="$COMPOSE_PROJECT" \\',
        '  CATALOG_COMPOSE_HTTP_PORT="$CATALOG_COMPOSE_HTTP_PORT" \\',
        '  CATALOG_COMPOSE_POSTGRES_PORT="$CATALOG_COMPOSE_POSTGRES_PORT" \\',
        '  CATALOG_UI_PORT="$CATALOG_UI_PORT" \\',
        '  CATALOG_COMPOSE_SUBNET="$CATALOG_COMPOSE_SUBNET" \\',
        '  DEMO_ARCHIVE_DIR="$DEMO_ARCHIVE_DIR" \\',
        '  DEMO_ARCHIVE_IP="$DEMO_ARCHIVE_IP" \\',
        '  DEMO_ARCHIVE_PORT="$DEMO_ARCHIVE_PORT" \\',
        '  DEMO_STATE_FILE="$DEMO_STATE_FILE" \\',
        '  DEMO_DATA_FILE="$DEMO_DATA_FILE" \\',
        '  DEMO_SAMPLE_SET="$DEMO_SAMPLE_SET" \\',
        '  DEMO_BASE_URL="http://127.0.0.1:$CATALOG_COMPOSE_HTTP_PORT" \\',
        '  DEMO_ARCHIVE_ORIGIN="https://$DEMO_ARCHIVE_IP:8443" \\',
        '  DEMO_ARCHIVE_TRUST_FILE="$DEMO_ARCHIVE_DIR/cert.pem" \\',
        '  DEMO_EXTEND=false \\',
        '  LOCAL_WEBHOOKS="${LOCAL_WEBHOOKS:-on}" \\',
        '  LOCAL_MONITORING="${LOCAL_MONITORING:-off}" \\',
        '  LOCAL_EMBEDDINGS="${LOCAL_EMBEDDINGS:-off}" \\',
        '  LOCAL_DEMO_RUNTIMES="${LOCAL_DEMO_RUNTIMES:-off}" \\',
        '  DEMO_RUNTIME_DIR="${DEMO_RUNTIME_DIR:-$PWD/output/demo/$COMPOSE_PROJECT/runtimes}" \\',
        '  DEMO_A2A_PORT="${DEMO_A2A_PORT:-19443}" \\',
        '  DEMO_MCP_PORT="${DEMO_MCP_PORT:-19444}"',
      ].join('\n'), 'make demo-check\nmake demo-archives-prepare\nmake compose-up LOCAL_UI=on LOCAL_DEMO_ARCHIVES=on\n# Wait for /readyz before using the standalone loader.\nmake demo-seed']],
    ], ['start-the-catalog-with-compose-and-load-sample-data', 'start-an-in-memory-catalog-and-load-sample-data'],
    ['Both sequences load records and activate a public snapshot', 'Do not reuse or reset the retained default development project']],
    ['contributing/documentation', [
      [['Site root', 'Subpath'], ['make docs-build\nmake docs-preview', 'make docs-build DOCS_BASE=/catalog/\nmake docs-preview DOCS_BASE=/catalog/']],
    ], ['install-build-and-preview-locally', 'content-components'],
    ['make docs-install', 'Stop your previous preview', 'Preview serves existing output']],
  ];
  const insideTabs = node => {
    for (let parent = node.parentNode; parent; parent = parent.parentNode) if (parent.tagName === 'starlight-tabs') return true;
    return false;
  };
  for (const [route, groups, anchors, visible] of tabCases) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    assert.equal(page.tabs.length, groups.length, `Selected tab groups: ${route}`);
    groups.forEach(([labels, commands], index) => {
      const group = page.tabs[index];
      const controls = elements(group, 'a').filter(node => attr(node, 'role') === 'tab');
      const panels = group.childNodes.filter(node => attr(node, 'role') === 'tabpanel');
      assert.equal(attr(group, 'data-sync-key'), undefined, `Independent choice: ${route}`);
      assert.deepEqual(controls.map(node => text(node).trim()), labels, `Tab labels: ${route}`);
      assert.equal(panels.length, labels.length, `Complete panels: ${route}`);
      panels.forEach((panel, panelIndex) => {
        assert.equal(attr(panel, 'aria-labelledby'), attr(controls[panelIndex], 'id'), 'Panel name comes from its tab');
        assert.equal(attr(controls[panelIndex], 'href'), '#' + attr(panel, 'id'), 'Tab targets its exact panel');
        assert.equal(attr(controls[panelIndex], 'aria-selected'), panelIndex === 0 ? 'true' : 'false', 'Only the first choice starts selected');
        assert.equal(attr(panel, 'hidden') !== undefined, panelIndex !== 0, 'Initial panel visibility');
        const copies = elements(panel, 'button').map(node => attr(node, 'data-code')?.replaceAll('\u007f', '\n'));
        assert(copies.some(command => command?.includes(commands[panelIndex])), `Commands in the correct panel: ${route} ${labels[panelIndex]}`);
        assert(!elements(panel, 'h2').length && !elements(panel, 'h3').length, 'Linked headings stay outside hidden panels');
      });
    });
    for (const id of anchors) {
      const heading = elements(page.document, 'h2').concat(elements(page.document, 'h3')).find(node => attr(node, 'id') === id);
      assert(heading && !insideTabs(heading), `Visible established anchor: ${route}#${id}`);
    }
    for (const phrase of visible) {
      assert(elements(page.document, 'p').concat(elements(page.document, 'pre')).some(node => !insideTabs(node) && text(node).includes(phrase)), `Visible common requirement: ${route}: ${phrase}`);
    }
  }
  const plugin = pages.get(path.join(dist, 'guides/agent-plugins', 'index.html'));
  assert.equal(plugin.trees.length, 1, 'One bounded Plugin package tree');
  const tree = plugin.trees[0];
  assert.notEqual(attr(tree, 'data-pagefind-ignore'), undefined, 'Native tree remains excluded from search');
  const summaries = elements(tree, 'summary').map(node => text(node).replace(/^Directory\s*/, '').trim());
  assert.deepEqual(summaries, ['release-workflow', 'skills', 'prepare-release'], 'Exact package directories');
  assert(elements(tree, 'details').every(node => attr(node, 'open') !== undefined), 'Package starts expanded');
  const filesInTree = elements(tree, 'li').filter(node => attr(node, 'class') === 'file').map(node => text(node).trim());
  assert.deepEqual(filesInTree, ['plugin.json', 'mcp.json', 'SKILL.md'], 'Exact package filenames');
  // Bind the displayed nesting to the maintained sample, not just leaf names.
  for (const file of elements(tree, 'li').filter(node => attr(node, 'class') === 'file')) {
    const segments = [text(file).trim()];
    for (let parent = file.parentNode; parent && parent !== tree; parent = parent.parentNode) {
      if (parent.tagName === 'details') {
        const summary = parent.childNodes.find(node => node.tagName === 'summary');
        segments.unshift(text(summary).replace(/^Directory\s*/, '').trim());
      }
    }
    assert(fs.statSync(path.join(root, 'samples/packages', ...segments)).isFile(), `Sample path: ${segments.join('/')}`);
  }
  const pluginSource = fs.readFileSync(path.join(project, 'src/content/docs/guides/agent-plugins.mdx'), 'utf8');
  const prose = pluginSource.replace(/<FileTree>[\s\S]*?<\/FileTree>/g, '');
  for (const filename of ['plugin.json', 'mcp.json', 'skills/prepare-release/SKILL.md']) {
    assert(prose.includes('`' + filename + '`'), `Searchable package path: ${filename}`);
  }
  const manifestFrame = plugin.code[0].parentNode;
  assert(elements(manifestFrame, 'figcaption').some(node => text(node).trim() === 'plugin.json'), 'Visible manifest filename');
  const backupSteps = pages.get(path.join(dist, 'operations/backup-restore', 'index.html')).steps;
  assert.equal(backupSteps.length, 1, 'One backup Steps list');
  assert.equal(attr(backupSteps[0], 'role'), 'list', 'Backup list remains accessible');
  const items = backupSteps[0].childNodes.filter(node => node.tagName === 'li');
  const starts = ['Record the expected tenant inventory', 'On the source', 'Transfer the archive', 'On the empty destination', 'With the matching Catalog build', 'Follow the all-tenant verification', 'After reviewing current configuration'];
  assert.equal(items.length, starts.length, 'All seven backup steps');
  items.forEach((item, index) => assert(text(item).trimStart().startsWith(starts[index]), `Backup step ${index + 1}`));
  const docker = pages.get(path.join(dist, 'operations/docker', 'index.html'));
  assert.equal(docker.trees.length, 1, 'One illustrative Docker input tree');
  const hostTree = docker.trees[0];
  assert.notEqual(attr(hostTree, 'data-pagefind-ignore'), undefined, 'Docker tree remains excluded from search');
  assert.deepEqual(elements(hostTree, 'summary').map(node => text(node).replace(/^Directory\s*/, '').trim()), ['$CATALOG_INPUTS/'], 'Tree identifies the chosen host directory');
  assert(elements(hostTree, 'details').every(node => attr(node, 'open') !== undefined), 'Host inputs start expanded');
  const hostFiles = ['config.json', 'runtime.env', 'db-ca.pem', 'owner.dsn', 'api.dsn', 'worker.dsn'];
  assert.deepEqual(elements(hostTree, 'li').filter(node => attr(node, 'class') === 'file').map(node => text(node).trim()), hostFiles, 'Exactly six host input files');
  const dockerSource = fs.readFileSync(path.join(root, entries.find(entry => entry.route === 'operations/docker').source), 'utf8');
  const dockerProse = dockerSource.replace(/<FileTree>[\s\S]*?<\/FileTree>/g, '');
  for (const filename of hostFiles) assert(dockerProse.includes('`' + filename + '`'), `Searchable host filename: ${filename}`);
  assert(dockerSource.indexOf('<FileTree>') > dockerSource.indexOf('cat > "$CATALOG_INPUTS/runtime.env"'), 'Tree follows the environment-file procedure');
  assert(dockerSource.indexOf('</FileTree>') < dockerSource.indexOf('## Establish database connectivity'), 'Tree precedes database connectivity');
  const repair = pages.get(path.join(dist, 'operations/search-projection-repair', 'index.html'));
  assert.equal(repair.steps.length, 1, 'One projection-repair Steps list');
  assert.equal(attr(repair.steps[0], 'role'), 'list', 'Repair sequence remains accessible');
  const repairItems = repair.steps[0].childNodes.filter(node => node.tagName === 'li');
  const repairStarts = ['Review the migration plan', 'Run one bounded batch', 'Review the JSON result', 'Repeat the bounded command', 'Restart the upgraded processes'];
  assert.equal(repairItems.length, repairStarts.length, 'All five repair steps');
  repairItems.forEach((item, index) => {
    assert(text(item).trimStart().startsWith(repairStarts[index]), `Repair step ${index + 1}`);
    assert.equal(elements(item, 'h2').length + elements(item, 'h3').length, 0, 'Repair headings stay outside Steps');
  });
  const labels = [
    ['reference/configuration', ['Backend JSON fragment', 'Validate backend environment overrides', 'Helm values fragment: backend and UI limits']],
    ['operations/docker', ['Create runtime.env']],
  ];
  for (const [route, titles] of labels) {
    const page = pages.get(path.join(dist, route, 'index.html'));
    const captions = elements(page.document, 'figcaption').map(node => text(node).trim());
    for (const title of titles) assert.equal(captions.filter(caption => caption === title).length, 1, `One example label: ${route}: ${title}`);
  }
  for (const source of downloads) assert.deepEqual(fs.readFileSync(path.join(dist,'generated',source)),fs.readFileSync(path.join(root,source)),`Download drift ${source}`);
  const contract = yaml(fs.readFileSync(path.join(root,'api/openapi/control-v1.yaml'),'utf8'));
  const operations = Object.values(contract.paths).flatMap(item=>Object.entries(item).filter(([method])=>['get','post','put','patch','delete','head','options','trace'].includes(method)).map(([,operation])=>operation));
  // The plugin generates one operation route per unique operationId.
  const apiPages = [...pages.keys()].filter(f=>f.includes('/dist/api/'));
  for (const operation of operations) {
    const slug = operation.operationId.toLowerCase();
    assert(apiPages.some(f=>path.dirname(f).split('/').at(-1).toLowerCase() === slug),`Unrendered API operation: ${operation.operationId}`);
  }
  assert(files.some(f=>f.endsWith('/pagefind/pagefind.js')),'Local Pagefind bundle missing');
  assert(!files.some(f=>f.endsWith('.map') || /\/(implementation-status\.md|todo|todo\.md|done\.md|deferred\.md|continue\.md|AGENTS\.md|output|coverage|architecture\.html)(\/|$)/.test(f)),'Excluded artifact or source map shipped');
  const allowed = new Set(entries.map(e=>path.join(dist,e.route,'index.html')));
  for (const page of pages.keys()) assert(allowed.has(page) || page.includes('/dist/api/') || page === path.join(dist,'404.html'),`Unexpected page: ${page}`);
  console.log(`Checked ${pages.size} HTML pages, ${operations.length} operations, exact downloads, assets and anchors at ${base}`);
}

for (const base of ['/catalog-check/','/']) {
  execFileSync('npm',['run','build'],{cwd:project,stdio:'inherit',env:{...process.env,DOCS_BASE:base,ASTRO_TELEMETRY_DISABLED:'1'}});
  inspect(base);
}
