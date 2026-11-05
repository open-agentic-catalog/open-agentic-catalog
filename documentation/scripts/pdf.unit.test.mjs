import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { parse } from 'parse5';
import { bundle, manifestName } from './bundle.mjs';
import { pageInventory, localTarget, chapter, printChapter, assemble, styleInputs, elements, text } from './pdf.mjs';
import { readBuild } from './pdf-export.mjs';

const files = new Set(['index.html', 'guide/index.html', 'api/index.html', 'api/operations/lastoperation/index.html', '404.html', 'diagram.svg', 'generated/example.json', 'style.css']);
const options = {base:'/book/', files, manualRoutes:['', 'guide'], operationIds:['LastOperation']};
function html(title, content = '') {
  return '<html><head><link rel="stylesheet" href="/book/style.css"><style>.note{color:blue}</style></head><body>' +
    '<nav class="sidebar"><details><summary>Closed group</summary><a href="/book/guide/#same">Guide</a><a href="/book/guide/#other">Guide section</a></details>' +
    '<a href="/book/">Home</a><a href="/book/api/">API</a><a href="/book/api/operations/lastoperation/">Last</a></nav>' +
    '<main><h1 id="_top">' + title + '</h1><div class="sl-markdown-content">' + content + '</div></main></body></html>';
}
function pages(content = '') {
  const inventory = pageInventory(html('Home'), options);
  return inventory.map((entry, index) => chapter(html(entry.route || 'Home', '<h2 id="same">Same heading</h2><h2 id="other">Other heading</h2>' + content), entry, index));
}

test('inventory follows rendered closed-group order and retains distinct fragment targets once', () => {
  const result = pageInventory(html('Home'), options);
  assert.deepEqual(result.map(page => page.file), ['guide/index.html', 'index.html', 'api/index.html', 'api/operations/lastoperation/index.html']);
  assert.deepEqual(result[0].fragments, ['same', 'other']);
});

test('inventory refuses missing pages, omitted final operations and unregistered HTML', () => {
  const missing = new Set(files); missing.delete('guide/index.html');
  assert.throws(() => pageInventory(html('Home'), {...options,files:missing}), /missing/i);
  assert.throws(() => pageInventory(html('Home').replace('<a href="/book/api/operations/lastoperation/">Last</a>', ''), options), /Incomplete/);
  const privateFiles = new Set(files); privateFiles.add('private/index.html');
  assert.throws(() => pageInventory(html('Home'), {...options,files:privateFiles}), /Unregistered/);
  assert.throws(() => pageInventory(html('Home'), {...options,manualRoutes:['','guide','guide']}), /Duplicate/);
});

test('inventory reconciles generated tag routes as well as operation pages', () => {
  const tagged = new Set(files); tagged.add('api/operations/tags/my-access/index.html');
  const home = html('Home').replace('</nav>', '<a href="/book/api/operations/tags/my-access/">My access</a></nav>');
  assert.equal(pageInventory(home, {...options,files:tagged,apiTags:['My access']}).length, 5);
  assert.throws(() => pageInventory(home, {...options,files:tagged}), /Unregistered/);
});

test('local references use their original page and reject paths outside the hosting base', () => {
  assert.deepEqual(localTarget('../guide/#caf%C3%A9','api/','/book/'), {file:'guide/index.html',fragment:'café'});
  assert.deepEqual(localTarget('#same','guide/','/book/'), {file:'guide/index.html',fragment:'same'});
  assert.equal(localTarget('https://example.org/reference','guide/','/book/'), null);
  assert.throws(() => localTarget('/elsewhere/','guide/','/book/'), /Invalid local/);
  assert.throws(() => localTarget('/book/guide/?private=1','guide/','/book/'), /Invalid local/);
  assert.throws(() => localTarget('javascript:alert(1)','guide/','/book/'), /scheme/);
});

test('chapter admission requires an actual title, body and unambiguous anchors', () => {
  const page = {file:'guide/index.html',route:'guide/',fragments:['same']};
  assert.throws(() => chapter(html('Title','<h2 id="same">A</h2><h2 id="same">B</h2>'),page,0), /Duplicate chapter anchor/);
  assert.throws(() => chapter(html('Title'),page,0), /Missing sidebar fragment/);
  assert.throws(() => chapter(html('Title').replace('sl-markdown-content','missing-content'),page,0), /Incomplete manual chapter/);
});

test('cross-page and SVG references retain the correct chapter after namespace changes', () => {
  const values = pages('<a href="/book/#same">Home section</a><a href="#same">This section</a>' +
    '<svg aria-labelledby="drawing"><title id="drawing">Drawing</title><defs><linearGradient id="shade"></linearGradient></defs><path fill="url(#shade)"></path><use href="#drawing"></use></svg>');
  const result = printChapter(values[0],values,options).html;
  assert.match(result,/href="#pdf-chapter-1--same"/);
  assert.match(result,/href="#pdf-chapter-0--same"/);
  assert.match(result,/aria-labelledby="pdf-chapter-0--drawing"/);
  assert.match(result,/url\(#pdf-chapter-0--shade\)/);
  assert.match(result,/href="#pdf-chapter-0--drawing"/);
  const second = printChapter(values[1],values,options).html;
  assert.match(second,/url\(#pdf-chapter-1--shade\)/);
});

test('all tab alternatives, disclosure content and exact code text survive printing', () => {
  const source = '<div role="tablist"><button role="tab" id="tab-a">Compose</button><button role="tab" id="tab-b">Helm</button></div>' +
    '<div role="tabpanel" aria-labelledby="tab-a">Compose steps</div><div role="tabpanel" aria-labelledby="tab-b" hidden>Helm steps</div>' +
    '<details><summary>Resume</summary><p>Interrupted load steps</p></details><pre><code>  a &lt; b\n\tUnicode: café \uD83D\uDE00\n</code></pre>';
  const values=pages(source), result=printChapter(values[0],values,options).html, parsed=parse(result);
  assert.match(result,/<strong>Compose<\/strong>/);
  assert.match(result,/<strong>Helm<\/strong>/);
  assert.match(result,/Helm steps/);
  assert.match(result,/pdf-disclosure/);assert.match(result,/pdf-summary/);
  assert.doesNotMatch(result,/<details|<summary/);
  assert.doesNotMatch(result,/ hidden|<button|role="tablist"/);
  assert.equal(text(elements(parsed,node=>node.tagName==='code')[0]),'  a < b\n\tUnicode: café \uD83D\uDE00\n');
});

test('download references remain readable without a temporary-server hyperlink', () => {
  const values=pages('<a href="/book/generated/example.json">Example</a><a href="https://example.org/reference">External reference</a><img src="/book/diagram.svg">');
  const result=printChapter(values[0],values,options);
  assert.equal(result.downloads,1);
  assert.match(result.html,/HTML download: \/book\/generated\/example.json/);
  assert.doesNotMatch(result.html,/href="\/book\/generated/);
  assert.match(result.html,/href="https:\/\/example.org\/reference"/);
  assert.match(result.html,/src="\/book\/diagram.svg"/);
});

test('missing anchors, external images and absent local images are refused before rendering', () => {
  for(const [content,pattern] of [['<a href="#absent">Missing</a>',/Unresolved/],['<img src="https://example.invalid/image.png">',/External print asset/],['<img src="/book/absent.png">',/Missing local print asset/]]) {
    const values=pages(content);assert.throws(()=>printChapter(values[0],values,options),pattern);
  }
});

test('print styles come only from owned assets and the original rendered CSS', () => {
  assert.deepEqual(styleInputs(html('Title'),'','/book/',files), {styles:['.note{color:blue}'],links:['/book/style.css']});
  assert.throws(()=>styleInputs(html('Title').replace('/book/style.css','https://example.invalid/style.css'),'','/book/',files),/Missing print stylesheet/);
  assert.deepEqual(styleInputs(html('Title','<svg><style>.shape{fill:url(#shade)}</style></svg>'),'','/book/',files).styles,['.note{color:blue}']);
});

test('contents and original license text are present in the complete print document', () => {
  const values=pages(), rendered=values.map(value=>printChapter(value,values,options));
  const output=assemble(values,rendered,{styles:[],links:[],css:'',license:'Original Apache text',notices:'Original third-party notice',version:'0.1.0'});
  assert.match(output,/<title>Open Agentic Catalog manual - 0.1.0<\/title>/);
  assert.match(output,/<a href="#pdf-chapter-3">api\/operations\/lastoperation\/<\/a>/);
  assert.match(output,/Original Apache text/);assert.match(output,/Original third-party notice/);
});

test('build verification refuses changed files, missing files and a different package lock', t => {
  const directory=fs.mkdtempSync(path.join(os.tmpdir(),'manual-seal-unit-'));t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
  fs.writeFileSync(path.join(directory,'index.html'),'<html><head></head><body>Unit fixture</body></html>');
  const lock=Buffer.from('owned lock');
  fs.writeFileSync(path.join(directory,manifestName),JSON.stringify(bundle(directory,'/',lock))+'\n');
  assert.equal(readBuild(directory,lock).manifest.base,'/');
  assert.throws(()=>readBuild(directory,Buffer.from('other lock')),/Incomplete or changed/);
  fs.writeFileSync(path.join(directory,'index.html'),'changed');
  assert.throws(()=>readBuild(directory,lock),/Incomplete or changed/);
  fs.unlinkSync(path.join(directory,'index.html'));
  assert.throws(()=>readBuild(directory,lock),/Incomplete/);
});


test('long notices stay exact across bounded layout blocks and API media labels survive', () => {
  const values=pages('<div role="tabpanel" data-openapi-content-type="application/json">API body</div>');
  const rendered=values.map(value=>printChapter(value,values,options));
  assert.match(rendered[0].html,/<strong>application\/json<\/strong>/);
  const notices=Array.from({length:255},(_,i)=>i+' Original notice line').join('\n');
  const output=assemble(values,rendered,{styles:[],links:[],css:'',license:'Exact license',notices,version:'0.1.0'});
  const appendix=elements(parse(output),node=>(node.attrs??[]).some(a=>a.name==='class'&&a.value==='pdf-notices'))[0];
  const blocks=elements(appendix,node=>node.tagName==='pre');
  assert.equal(blocks.length,3);
  assert.equal(blocks.map(text).join(''),'Exact license\n\n'+notices);
});


test('rendered code retains complete line breaks, indentation and blank lines without token wrappers', () => {
  const content='<pre><code><div class="ec-line"><span class="gutter">1</span><div class="code"><span>  const a = </span><span>1;</span></div></div>'+
    '<div class="ec-line"><div class="code"></div></div><div class="ec-line"><div class="code"><span>\tb &lt; a</span></div></div></code></pre>';
  const values=pages(content), output=printChapter(values[0],values,options).html;
  const code=elements(parse(output),node=>node.tagName==='code')[0];
  assert.equal(text(code),'  const a = 1;\n\n\tb < a');
  assert.equal(code.childNodes.length,1);
  assert.throws(()=>{const bad=pages('<pre><code><div class="ec-line">Unknown renderer format</div></code></pre>');printChapter(bad[0],bad,options);},/Unrecognized rendered code line/);
});

test('print code cards preserve captions, every anchor and exact text outside widget styling', () => {
  const content='<div class="expressive-code" id="widget"><figure class="frame" id="frame"><figcaption><span class="title" id="caption">example.sh</span></figcaption>'+
    '<pre id="pre"><code id="source">  echo "unchanged"\n\tend\n</code></pre></figure></div><a href="#source">Code</a>';
  const values=pages(content), result=printChapter(values[0],values,options).html, parsed=parse(result);
  assert.doesNotMatch(result,/class="expressive-code"|class="frame"/);
  assert.match(result,/<figcaption>example.sh<\/figcaption>/);
  assert.equal(text(elements(parsed,node=>node.tagName==='pre')[0]),'  echo "unchanged"\n\tend\n');
  for(const id of ['widget','frame','caption','pre','source'])assert.match(result,new RegExp('id="pdf-chapter-0--'+id+'"'));
  assert.match(result,/href="#pdf-chapter-0--source"/);
});
