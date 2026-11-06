// Isolated regressions for reader projection, link policy and rendered SVG contracts.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { basePath, validateEntries, entries, resolveLink, omittedSections, omittedAnchors, assertLocalRefs, assertHandbookNavigation, remarkRepositoryLinks, projectManualLinks, projectDiagrams, project, pagePath, root } from './site.mjs';
import { repairHeaderAnchors } from './openapi-anchors.mjs';
import { parseFragment } from 'parse5';
import { assertDiagram } from './diagrams.mjs';

test('base paths accept root and portable nested segments, not URL or traversal inputs',()=>{
  for (const base of ['/','/catalog/','/team/catalog-1/']) assert.equal(basePath(base),base);
  for (const base of ['','catalog','/catalog','//','https://host/','/../','/a%2fb/','/a?b/','/a b/']) assert.throws(()=>basePath(base));
});
test('rendered handbook navigation rejects missing and duplicate pages at either base',()=>{
  const manifest = [{route:''},{route:'operations/overview'}];
  for (const base of ['/','/nested/catalog/']) {
    const links = [base,base+'operations/overview/'];
    assertHandbookNavigation([...links,base+'api/operations/example/'],manifest,base);
    assert.throws(()=>assertHandbookNavigation(links.slice(1),manifest,base),/found 0/);
    assert.throws(()=>assertHandbookNavigation([...links,links[1]],manifest,base),/found 2/);
    assert.throws(()=>assertHandbookNavigation(['/wrong/',links[1]],manifest,base),/found 0/);
  }
});
test('manifest rejects duplicate, private, reserved and unsafe routes',()=>{
  validateEntries(entries);
  const entry = {source:'documentation/src/content/docs/example.md',route:'example'};
  assert.throws(()=>validateEntries([entry,entry]));
  assert.throws(()=>validateEntries([{...entry,aliases:['old.md']},{source:'other.md',route:'other',aliases:['old.md']}]));
  for (const source of ['implementation-status.md','todo.md','done.md','deferred.md','todo/README.md','todo/implementation-status.md','todo/todo.md','todo/done.md','todo/deferred.md','todo/research.md','continue.md','AGENTS.md','todo/records/testing.md','output/private.md','../private.md','/private.md']) assert.throws(()=>validateEntries([{...entry,source}]));
  for (const route of ['../escape','api/collision','/absolute']) assert.throws(()=>validateEntries([{...entry,route}]));
});
test('canonical repository links resolve to local routes and preserve fragments',()=>{
  assert.equal(resolveLink('documentation/README.md','src/content/docs/operations/webhooks.mdx#signatures','/nested/'),'/nested/operations/webhooks/#signatures');
  assert.equal(resolveLink('documentation/README.md','../api/openapi/control-v1.yaml','/nested/'),'/nested/generated/api/openapi/control-v1.yaml');
  for (const file of ['README.md','todo.md','done.md','deferred.md','implementation-status.md']) assert.equal(resolveLink('documentation/README.md','../todo/'+file),null);
  assert.equal(resolveLink('documentation/README.md','../cmd/catalogd/main.go'),null);
  assert.equal(resolveLink('documentation/README.md','#local'),'#local');
  assert.equal(resolveLink('documentation/README.md','https://example.org/spec'),'https://example.org/spec');
});

test('manual-link comments expand only registered manual destinations',()=>{
  const entry = entries.find(item => item.source === 'extensions/vscode/README.md');
  const marker = target => `<!-- manual-link: [Desktop setup](${target}) -->`;
  const target = '../../documentation/src/content/docs/operations/oidc-keycloak.md#connect-the-desktop-client';
  const plain = 'Read **Keycloak > Connect the desktop client**.\n\n<!-- Keep this ordinary comment. -->\n';
  assert.equal(projectManualLinks(entry, plain), plain);
  assert.equal(projectManualLinks(entry, plain + marker(target)), plain + `[Desktop setup](${target})`);
  for (const rejected of ['https://example.com/setup.md', '/guides/setup.md', '#local', '../../todo/todo.md', '../../LICENSE', '../../api/examples/hybrid-search.json', '../../documentation/src/content/docs/missing.md', '../../documentation/src/content/docs/operations/oidc-keycloak.md?private=value']) {
    assert.throws(()=>projectManualLinks(entry,marker(rejected)),/Invalid manual-link/);
  }
  assert.throws(()=>projectManualLinks({...entry,projection:false},marker(target)),/Invalid manual-link/);
  assert.equal(projectManualLinks(entry,marker(target).replace('Desktop setup','x'.repeat(160))),`[${'x'.repeat(160)}](${target})`);
  assert.throws(()=>projectManualLinks(entry,marker(target).replace('Desktop setup','x'.repeat(161))),/Invalid manual-link/);
  assert.throws(()=>projectManualLinks(entry,marker('./'.repeat(600)+target)),/Invalid manual-link/);
  assert.throws(()=>projectManualLinks(entry,marker(target).replace('Desktop setup','<script>')),/Invalid manual-link/);
  for (const malformed of ['<!-- manual-link: missing -->', '<!-- manual-link: [Setup](target.md) --> extra', '<!-- manual-link:\n[Setup](target.md)\n-->']) {
    assert.throws(()=>projectManualLinks(entry,malformed),/Malformed manual-link/);
  }
});

test('the offline VS Code guide has three site-only links and keeps visible links inside its package',()=>{
  const entry = entries.find(item => item.source === 'extensions/vscode/README.md');
  const filename = path.join(root,entry.source);
  const before = fs.readFileSync(filename);
  const source = before.toString('utf8');
  const targets = [...source.matchAll(/^<!-- manual-link: \[[^\]]+\]\(([^)]+)\) -->$/gm)].map(match=>match[1]);
  assert.deepEqual(targets,[
    '../../documentation/src/content/docs/guides/two-catalog-federation.mdx',
    '../../documentation/src/content/docs/operations/oidc-keycloak.md#connect-the-desktop-client',
    '../../documentation/src/content/docs/contributing/build-and-release.md#optional-vs-code-package',
  ]);
  const offline = source.replace(/<!--[\s\S]*?-->/g,'');
  for (const breadcrumb of ['Deploy and operate > Two-Catalog federation tutorial', 'Keycloak > Connect the desktop client', 'Develop and contribute > Build and release > Optional VS Code package']) {
    assert(offline.includes(breadcrumb),`Visible offline reference: ${breadcrumb}`);
  }
  assert(offline.includes('vscode://open-agentic-catalog.open-agentic-catalog/oidc/*'));
  const visible = [...offline.matchAll(/\[[^\]\n]+\]\(([^)\n]+)\)/g)].map(match=>match[1]);
  for (const target of visible.filter(value=>!value.startsWith('#') && !/^[a-z][a-z\d+.-]*:/i.test(value))) {
    assert(target.startsWith('media/'),`Offline relative link stays in the package: ${target}`);
    assert(fs.statSync(path.join(path.dirname(filename),target)).isFile(),target);
  }
  const imageTargets = ['media/catalog-preview.png','media/my-tools-installed.png','media/mcp-selection-preview.png','media/agent-conversation.png'];
  const images = [...offline.matchAll(/!\[[^\]\n]+\]\((media\/[^)\n]+)\)/g)].map(match=>match[1]);
  const fullSize = [...offline.matchAll(/(?<!!)\[[^\]\n]+\]\((media\/[^)\n]+)\)/g)].map(match=>match[1]);
  assert.deepEqual(images,imageTargets,'Exactly the four maintained illustrations');
  assert.deepEqual(fullSize,imageTargets,'Each illustration has one explicit full-size link');
  for (const base of ['/','/team/catalog/']) {
    for (const target of imageTargets) {
      assert.equal(resolveLink(entry.source,target,base),base+'generated/extensions/vscode/'+target);
    }
  }
  const projected = projectManualLinks(entry,source);
  assert(!projected.includes('<!-- manual-link:'));
  for (const [index,route] of ['guides/two-catalog-federation/','operations/oidc-keycloak/#connect-the-desktop-client','contributing/build-and-release/#optional-vs-code-package'].entries()) {
    assert(projected.includes(`](${targets[index]})`));
    for (const prefix of ['/','/team/catalog/']) assert.equal(resolveLink(entry.source,targets[index],prefix),prefix+route);
  }
  assert.deepEqual(fs.readFileSync(filename),before,'Projection does not rewrite the offline source');
});

test('architecture and decisions resolve at either base while developer status stays excluded',()=>{
  const source = 'documentation/src/content/docs/architecture/overview.mdx';
  for (const base of ['/','/team/catalog/']) {
    assert.equal(resolveLink(source,'deployment.mdx',base),base+'architecture/deployment/');
    assert.equal(resolveLink(source,'decisions/0018-optional-catalog-browser-client.md',base),base+'architecture/decisions/0018-optional-catalog-browser-client/');
    assert.equal(resolveLink(source,'../../../../../todo/implementation-status.md',base),null);
    assert.equal(resolveLink(source,'../../../../../todo/records/testing.md',base),null);
  }
  const decisions = fs.readdirSync(path.join(project,'src/content/docs/architecture/decisions')).filter(f=>f.endsWith('.md'));
  for (const file of decisions) {
    const entry = entries.find(e=>e.source === 'documentation/src/content/docs/architecture/decisions/'+file);
    assert(entry && !entry.projection && !entry.omitSections,`Decision must be authored and rendered in full: ${file}`);
  }
});

test('SDK source guides publish as linked pages rather than repository-only text',()=>{
  for (const [source, route] of [
    ['sdk/README.md','sdk/overview'],
    ['sdk/go/README.md','sdk/go'],
    ['sdk/python/README.md','sdk/python'],
    ['sdk/typescript/README.md','sdk/typescript'],
    ['sdk/rust/README.md','sdk/rust'],
  ]) {
    const entry = entries.find(e=>e.source === source);
    assert(entry?.projection && entry.route === route);
    for (const base of ['/','/team/catalog/']) {
      assert.equal(resolveLink('documentation/src/content/docs/guides/integrating.mdx','../../../../../'+source,base),base+route+'/');
    }
    const target = source === 'sdk/README.md' ? 'go/README.md#install-the-sdk' : '../README.md#choose-a-language';
    const node = {type:'link',url:target,children:[{type:'text',value:'SDK guide'}]};
    remarkRepositoryLinks()({type:'root',children:[node]},{history:[path.join(root,pagePath(entry))]});
    assert.equal(node.type,'link');
    assert(node.url.endsWith(source === 'sdk/README.md' ? '/sdk/go/#install-the-sdk' : '/sdk/overview/#choose-a-language'));
  }
  for (const base of ['/','/team/catalog/']) {
    assert.equal(resolveLink('sdk/README.md','python/README.md#read-ard-discovery',base),base+'sdk/python/#read-ard-discovery');
    assert.equal(resolveLink('sdk/go/README.md','../../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-go',base),base+'guides/integrating/#search-one-page-with-go');
  }
});

test('reviewed UI screenshots resolve at either base and unlisted images fail closed',()=>{
  const source = 'documentation/src/content/docs/guides/catalog-ui.mdx';
  for (const base of ['/','/team/catalog/']) {
    for (const name of ['regular-sign-in','local-keycloak-sign-in']) {
      assert.equal(resolveLink('documentation/src/content/docs/operations/oidc-keycloak.md',`../../../../assets/catalog-ui/${name}.png`,base),`${base}generated/documentation/assets/catalog-ui/${name}.png`);
    }
    for (const name of ['catalog-browse','catalog-table-sorted','catalog-comparison','catalog-record','catalog-filters','catalog-preview','catalog-api-reference','catalog-api-areas','catalog-api-definitions','release-history','release-details','development-sign-in','resource-ratings','help-documentation','about','troubleshooting','publication-contents','getting-started','tenant-settings','document-first-authoring','draft-import-preview','draft-import-destination','authoring-protocol-choices','authoring-file-stages','authoring-mobile-preview','mcp-field-authoring','skill-field-authoring','plugin-field-authoring','guided-listing-details','guided-review-approved','version-comparison','listing-lifecycle','authoring-exact-identifier','review-queue-context','overview-review-activity','overview-operations','overview-webhooks','overview-administration','overview-content','overview-service-activity','my-work','saved-search','access-roles','my-access-reader','ans-assessment','artwork-gallery']) {
      assert.equal(resolveLink(source,`../../../../assets/catalog-ui/${name}.png`,base),`${base}generated/documentation/assets/catalog-ui/${name}.png`);
    }
  }
  for (const target of ["documentation/assets/catalog-ui/access-assignment-confirmation.png", "documentation/assets/catalog-ui/member-access-contributions.png", "documentation/assets/catalog-ui/audit-investigation.png", "documentation/assets/catalog-ui/webhook-delivery-history.png", "documentation/assets/catalog-ui/publication-snapshot-comparison.png", "documentation/assets/catalog-ui/job-detail-recovery.png", "documentation/assets/catalog-ui/service-information-processes.png", "documentation/assets/monitoring/catalog-database-worker.png", "documentation/assets/monitoring/prometheus-target-health.png", "documentation/assets/monitoring/grafana-dashboard-import.png", "documentation/assets/sonarqube/project-overview.png", "extensions/vscode/media/mcp-selection-preview.png", "extensions/vscode/media/my-tools-installed.png", "documentation/assets/catalog-ui/source-retained-occurrence.png", "documentation/assets/catalog-ui/source-adoption-preparation.png", "documentation/assets/monitoring/jaeger-job-attempt.png", "documentation/assets/monitoring/jaeger-job-cause.png", "documentation/assets/contextforge/upstream-registrations.png"]) {
    const relative = '../../../../../' + target;
    for (const base of ['/','/team/catalog/']) {
      assert.equal(resolveLink(source,relative,base),base+'generated/'+target);
    }
  }
  const image = {type:'image',url:'../../../../assets/catalog-ui/catalog-browse.png',alt:'Catalog demo'};
  const tree = {type:'root',children:[image]};
  const transform = remarkRepositoryLinks();
  transform(tree,{history:[path.join(root,source)]});
  assert(image.url.endsWith('/generated/documentation/assets/catalog-ui/catalog-browse.png'));
  assert.equal(image.alt,'Catalog demo');
  image.url = '../../../../../output/playwright/private.png';
  assert.throws(()=>transform(tree,{history:[path.join(root,source)]}),/Unlisted image/);
});

test('reviewed monitoring image and full-size link resolve at either base',()=>{
  const source = 'documentation/src/content/docs/guides/performance-and-monitoring.md';
  const target = '../../../../assets/monitoring/catalog-overview.png';
  for (const base of ['/','/team/catalog/']) {
    assert.equal(resolveLink(source,target,base),`${base}generated/documentation/assets/monitoring/catalog-overview.png`);
  }
  const image = {type:'image',url:target,alt:'Grafana Catalog overview with synthetic traffic'};
  const link = {type:'link',url:target,children:[{type:'text',value:'Full-size screenshot'}]};
  remarkRepositoryLinks()({type:'root',children:[image,link]},{history:[path.join(root,source)]});
  assert.equal(image.url,link.url);
  assert.equal(image.alt,'Grafana Catalog overview with synthetic traffic');
  image.url = '../../../../assets/monitoring/unreviewed.png';
  assert.throws(()=>remarkRepositoryLinks()({type:'root',children:[image]},{history:[path.join(root,source)]}),/Unlisted image/);
});

test('technical diagrams require accessible text and local marker references',()=>{
  const valid = '<svg role="img" aria-labelledby="title description"><title id="title">Command path</title><desc id="description">Request to commit.</desc><defs><marker id="arrow"></marker></defs><path marker-end="url(#arrow)"></path></svg>';
  const check = text => assertDiagram(parseFragment(text).childNodes[0]);
  check(valid);
  assert.throws(()=>check(valid.replace('title description','title description missing')),/Missing diagram label/);
  for (const broken of [valid.replace(' description',''),valid.replace('Request to commit.',''),valid.replace('url(#arrow)','url(#missing)'),valid.replace('</svg>','<script></script></svg>'),valid.replace('id="arrow"','id="title"')]) assert.throws(()=>check(broken));
  check(valid.replace('</svg>', '<use href="#arrow"></use></svg>'));
  for (const element of ['foreignObject', 'image']) {
    assert.throws(() => check(valid.replace('</svg>', `<${element}></${element}></svg>`)), /self-contained SVG/);
  }
  assert.throws(() => check(valid.replace('<svg ', '<svg onload="alert(1)" ')), /event handlers/);
  assert.throws(() => check(valid.replace('url(#arrow)', 'url(https://example.test/marker.svg#arrow)')), /URL must be local/);
  assert.throws(() => check(valid.replace('</svg>', '<use href="https://example.test/shape.svg#arrow"></use></svg>')), /reference must be local/);
  assert.throws(() => check(valid.replace('</svg>', '<use href="#missing"></use></svg>')), /Missing diagram marker/);
});

test('Markdown projections render only explicitly registered diagram components',()=>{
  const entry = {source:'DESIGN.md',route:'architecture/design',projection:true,diagrams:['WelcomeDeployment']};
  const source = '<!-- markdownlint-disable MD013 -->\n\nBefore.\n\n<!-- diagram: WelcomeDeployment -->\n\nAfter.\n';
  const rendered = projectDiagrams(entry,source);
  assert.equal(pagePath(entry),'documentation/src/content/docs/generated/architecture/design.mdx');
  assert.equal(pagePath({...entry,diagrams:undefined}),'documentation/src/content/docs/generated/architecture/design.md');
  assert(rendered.startsWith('import WelcomeDeployment from "../../../../components/architecture/WelcomeDeployment.astro";'));
  assert(rendered.includes('Before.\n\n<WelcomeDeployment />\n\nAfter.'));
  assert(!rendered.includes('markdownlint'));
  assert.equal(projectDiagrams({},'Ordinary Markdown.'),'Ordinary Markdown.');
  assert.throws(()=>projectDiagrams(entry,'Missing marker.'),/Expected one diagram marker/);
  assert.throws(()=>projectDiagrams(entry,source+source),/Expected one diagram marker/);
  assert.throws(()=>projectDiagrams({},source),/Unregistered diagram marker/);
  assert.throws(()=>projectDiagrams({...entry,diagrams:['../private']},source),/Invalid diagram component/);
});

test('plugin response-header anchors are unique and their permalinks agree',()=>{
  const heading = '<div><h4 id="headers">Headers</h4><a href="#headers">Link</a></div>';
  const fixed = repairHeaderAnchors(heading.repeat(3));
  assert.equal((fixed.match(/id="headers"/g)??[]).length,1);
  for (const suffix of ['-2','-3']) {
    assert(fixed.includes(`id="headers${suffix}"`));
    assert(fixed.includes(`href="#headers${suffix}"`));
  }
  assert.equal(repairHeaderAnchors(fixed),fixed);
});

test('omitted research sections also omit links to their nested headings',()=>{
  const omitted = omittedAnchors({omitSections:['Internal evidence']},'# Title\n\n## Public\n\n<a id="public-alias"></a>\n\n### Keep\n\n## Internal evidence\n\n<a id="old-result"></a>\n\n### Private result\n\n## End\n');
  assert.deepEqual([...omitted],['internal-evidence','private-result','old-result']);
  assert.equal(resolveLink('documentation/README.md','../todo/records/dependency-review.md#monitoring-artifact-completion-recheck-on-2026-09-07'),null);
  assert.equal(resolveLink('documentation/README.md','../todo/records/adr-verification.md#adr-0017-status-and-verification'),null);
  assert.equal(resolveLink('documentation/README.md','../todo/records/adr-verification.md#adr-0004-current-evidence-and-remaining-qualification'),null);
  assert.equal(resolveLink('documentation/README.md','../todo/implementation-status.md#verification-and-remaining-work'),null);
  assert.equal(resolveLink('documentation/README.md','../todo/implementation-status.md#known-limits-and-unavailable-behavior'),null);
});

test('repository references retain labels without fabricated links or build notices',()=>{
  const label = [{type:'inlineCode',value:'catalogd'},{type:'text',value:' command source'}];
  const tree = {type:'root',children:[{type:'paragraph',children:[{type:'link',url:'../../../../../cmd/catalogd/main.go',children:structuredClone(label)}]}]};
  remarkRepositoryLinks()(tree,{history:[path.join(project,'src/content/docs/reference/commands.md')]});
  const reference = tree.children[0].children[0];
  assert.equal(reference.url,undefined);
  assert.equal(reference.data.hName,'span');
  assert.deepEqual(reference.children,label);
});

test('section exclusions require exact unique headings instead of silently publishing renamed content',()=>{
  const entry = {source:'example.md',omitSections:['Internal evidence']};
  const text = '# Title\n\n## Public\n\nKeep.\n\n## Internal evidence\n\nPrivate.\n\n### Nested result\n\n## End\n';
  assert.deepEqual(omittedSections(entry,text),['## Internal evidence\n\nPrivate.\n\n### Nested result\n\n']);
  assert.deepEqual(omittedSections({},text),[]);
  assert.throws(()=>omittedSections(entry,text.replace('## Internal evidence','## Renamed evidence')),/found 0/);
  assert.throws(()=>omittedSections(entry,text+'\n## Internal evidence\n'),/found 2/);
});

test('OpenAPI inputs cannot resolve files or remote schemas',()=>{
  assertLocalRefs({schema:{$ref:'#/components/schemas/Problem'}});
  for (const ref of ['https://example.org/schema','file:///private','../schema.yaml',42]) assert.throws(()=>assertLocalRefs({schema:[{$ref:ref}]}));
});
