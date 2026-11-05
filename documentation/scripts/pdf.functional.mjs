// Explicit real-browser/PDF qualification; excluded from ordinary site and unit runs.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import { execFileSync } from 'node:child_process';
import { bundle, manifestName } from './bundle.mjs';
import { exportManual } from './pdf-export.mjs';
import { project, root } from './site.mjs';

const lock = fs.readFileSync(path.join(project, 'package-lock.json'));
const version = fs.readFileSync(path.join(root, 'VERSION'), 'utf8').trim();
const contract = {manualRoutes:['','chapter'],operationIds:['LastOperation']};
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jQ1sAAAAASUVORK5CYII=', 'base64');
const secondTitle='Second chapter with a long title that must preserve every word and space when its heading wraps across printed lines';
const requiredText = ['COMPOSE_ALTERNATIVE_COMPLETE','HELM_ALTERNATIVE_COMPLETE','INTERRUPTED_LOAD_RECOVERY_COMPLETE','SVG_LABEL_COMPLETE','LAST_API_OPERATION_COMPLETE','WIDE_TABLE_FINAL_CELL','SCHEMA_FIELD_DESCRIPTION_COMPLETE','Owned third-party fixture notice'];
const tokens = Array.from({length:130}, (_, i) => 'LONG_CODE_SEGMENT_' + i + '_' + 'x'.repeat(22));

function fixture(t, base = '/') {
  const directory = fs.mkdtempSync(path.join(root, 'output', 'pdf-functional-'));
  t.after(() => fs.rmSync(directory, {recursive:true,force:true}));
  const input = path.join(directory, 'build'), output = path.join(directory, 'result', 'manual.pdf');
  fs.mkdirSync(input);fs.mkdirSync(path.join(input,'generated'));
  fs.writeFileSync(path.join(input,'generated/LICENSE'),fs.readFileSync(path.join(root,'LICENSE')));
  fs.writeFileSync(path.join(input,'generated/third-party-notices.txt'),'Owned third-party fixture notice');
  fs.writeFileSync(path.join(input,'image.png'),png);
  const html = (title,body) => '<html><head><link rel="stylesheet" href="' + base + 'style.css"></head><body>' +
    '<nav class="sidebar"><a href="' + base + '">Home</a><details><summary>Closed group</summary><a href="' + base + 'chapter/#same">Chapter</a></details>' +
    '<a href="' + base + 'api/">API overview</a><a href="' + base + 'api/operations/lastoperation/">Last operation</a></nav>' +
    '<main><h1 id="_top">' + title + '</h1><div class="sl-markdown-content">' + body + '</div></main></body></html>';
  const content = '<h2 id="same">Duplicate heading</h2><div role="tablist"><button role="tab" id="compose">Compose</button><button role="tab" id="helm">Helm</button></div>' +
    '<div role="tabpanel" aria-labelledby="compose">COMPOSE_ALTERNATIVE_COMPLETE</div><div role="tabpanel" aria-labelledby="helm" hidden>HELM_ALTERNATIVE_COMPLETE</div>' +
    '<div class="sl-openapi-keys"><div class="sl-openapi-key"><strong>SCHEMA_FIELD_NAME</strong><p>SCHEMA_FIELD_DESCRIPTION_COMPLETE</p></div></div>' +
    '<details><summary>Resume an interrupted sample load</summary><p>INTERRUPTED_LOAD_RECOVERY_COMPLETE</p></details>' +
    '<div class="expressive-code"><figure class="frame"><pre><code>' + tokens.map((value,i)=>value+' '+('wrapped content '.repeat(12))+'END_OF_LINE_'+i).join('\n') + '</code></pre></figure></div><table><tr>' + [1,2,3,4,5,6].map(i=>'<th>Column '+i+'</th>').join('') + '</tr><tr>' +
    [1,2,3,4,5].map(i=>'<td>Long cell '+i+'</td>').join('') + '<td>WIDE_TABLE_FINAL_CELL</td></tr></table>' +
    '<svg class="architecture-svg" viewBox="0 0 640 180"><title id="drawing">SVG_LABEL_COMPLETE</title><defs><linearGradient id="shade"><stop stop-color="#164e80"/></linearGradient></defs>' +
    '<rect width="640" height="180" fill="url(#shade)"></rect><text x="20" y="80" fill="white">SVG_LABEL_COMPLETE</text></svg>' +
    '<img src="' + base + 'image.png" alt="Owned fixture pixel"><a href="' + base + '#same">First duplicate heading</a><a href="https://example.org/reference">External reference</a>';
  const write = (file, bytes) => {fs.mkdirSync(path.dirname(path.join(input,file)),{recursive:true});fs.writeFileSync(path.join(input,file),bytes);};
  write('index.html',html('First chapter','<h2 id="same">First duplicate heading</h2><p>Owned reader manual.</p>'));
  write('chapter/index.html',html(secondTitle,content));
  write('api/index.html',html('API overview','<p>Owned API overview.</p>'));
  write('api/operations/lastoperation/index.html',html('Last API operation','<p>LAST_API_OPERATION_COMPLETE</p>'));
  write('style.css','body{font-family:sans-serif}.sl-openapi-keys{display:grid;grid-template-columns:fit-content(30%) minmax(0,1fr)}.sl-openapi-key{display:grid;grid-template-columns:subgrid}.expressive-code .frame{display:grid;overflow:hidden}.expressive-code .frame pre{display:flex}.expressive-code pre>code{display:block;flex:1 0 100%;padding:1rem 0}');
  const seal = () => fs.writeFileSync(path.join(input,manifestName),JSON.stringify(bundle(input,base,lock))+'\n');
  seal();
  return {directory,input,output,base,write,seal,options:{input,output,contract}};
}

function inspectPDF(file) {
  const content = execFileSync('pdftotext',['-raw',file,'-'],{encoding:'utf8',timeout:30000,maxBuffer:8*1024*1024});
  const info = execFileSync('pdfinfo',[file],{encoding:'utf8',timeout:10000});
  const outline = execFileSync('mutool',['show',file,'outline'],{encoding:'utf8',timeout:10000,maxBuffer:1024*1024});
  const annotations = execFileSync('mutool',['show',file,'pages/*/Annots/*'],{encoding:'utf8',timeout:10000,maxBuffer:2*1024*1024});
  return {content,info,outline,annotations};
}

async function resourcesClosed(evidence) {
  assert.equal(evidence.cleanup,'passed');
  if(evidence.browserPid) assert.throws(()=>process.kill(evidence.browserPid,0),{code:'ESRCH'});
  if(evidence.assetPort) {
    await new Promise((resolve,reject)=>{
      const request=http.get('http://127.0.0.1:'+evidence.assetPort+'/',response=>{response.resume();reject(new Error('Owned asset server still listens'));});
      request.once('error',resolve);request.setTimeout(2000,()=>request.destroy(new Error('Cleanup probe timed out')));
    });
  }
}

for(const base of ['/','/nested/manual/']) {
  test('real PDF preserves complete content, destinations and input bytes at '+base,{timeout:90000},async t=>{
    const f=fixture(t,base), before=fs.readFileSync(path.join(f.input,manifestName));
    const result=await exportManual(f.options);
    assert.equal(result.status,'complete');assert.equal(result.pages,4);assert.equal(result.generatedAPIPages,2);
    assert.deepEqual(fs.readFileSync(path.join(f.input,manifestName)),before);
    assert.deepEqual(bundle(f.input,base,lock),JSON.parse(before));
    assert.equal(result.layout.apiFields,1);assert.equal(result.layout.unstackedAPIFields,0);assert.equal(result.layout.clippedBlocks,0);
    assert.equal(result.layout.panels,2);assert.equal(result.layout.diagrams,1);assert.equal(result.layout.images,1);
    await resourcesClosed(result);
    const native=inspectPDF(f.output), normalized=native.content.replace(/\s/g,'');
    for(const expected of [...requiredText,...tokens,...tokens.map((_,i)=>'END_OF_LINE_'+i)]) assert(normalized.includes(expected.replace(/\s/g,'')),expected);
    assert.equal(native.info.match(/^Title:\s+(.+)$/m)[1],'Open Agentic Catalog manual - '+version);
    assert.match(native.info,/Tagged:\s+yes/);
    assert.match(native.info,/Pages:\s+[1-9][0-9]*/);
    const titles=['First chapter',secondTitle,'API overview','Last API operation'];
    for(let i=0;i<titles.length;i++) {
      assert(native.outline.includes('"'+titles[i]+'"'),titles[i]);
      if(i) assert(native.outline.indexOf('"'+titles[i-1]+'"')<native.outline.indexOf('"'+titles[i]+'"'));
    }
    assert.match(native.annotations,/\/Dest/);
    assert.match(native.annotations,/https:\/\/example\.org\/reference/);
    assert(!native.annotations.includes('127.0.0.1:'+result.assetPort));
  });
}

test('real refusal, timeout and cancellation preserve a successful PDF and close owned resources',{timeout:180000},async t=>{
  const f=fixture(t), control=await exportManual(f.options), original=fs.readFileSync(f.output);
  await resourcesClosed(control);
  async function refused(options, expectedStage) {
    let failure;
    try {await exportManual({...f.options,...options});} catch(error) {failure=error;}
    assert(failure,'Expected an export refusal');
    assert.equal(failure.evidence.stage,expectedStage);
    await resourcesClosed(failure.evidence);
    assert.deepEqual(fs.readFileSync(f.output),original);
    assert(!fs.readdirSync(path.dirname(f.output)).some(name=>name.startsWith('.')&&name.endsWith('.pdf')));
    return failure;
  }
  fs.unlinkSync(path.join(f.input,'api/operations/lastoperation/index.html'));
  await refused({},'inputs');
  f.write('api/operations/lastoperation/index.html','<main>incomplete</main>');f.seal();
  await refused({},'prepare');
  f.write('api/operations/lastoperation/index.html','<html><head></head><body><main><h1>Last API operation</h1><div class="sl-markdown-content">Complete</div></main></body></html>');f.seal();
  f.write('style.css','@font-face{font-family:Missing;src:url("missing.woff")}body.pdf-book{font-family:Missing!important}');f.seal();
  await refused({},'layout');
  f.write('style.css','body{font-family:sans-serif}');f.seal();
  await refused({onStage:(stage,state)=>{if(stage==='render')setTimeout(()=>process.kill(state.browserPid,'SIGTERM'),1);}},'render');
  const timed=await refused({renderTimeoutMs:1},'render');assert.match(timed.message,/print deadline exceeded/);
  const controller=new AbortController();
  const canceled=await refused({signal:controller.signal,onStage:stage=>{if(stage==='render')setTimeout(()=>controller.abort(),1);}},'render');
  assert.match(canceled.message,/interrupted/);
  await refused({timeoutMs:1},'browser');
  await refused({onStage:stage=>{if(stage==='verify')f.write('style.css','changed after rendering');}},'verify');
});

test('external print assets make no contact with an owned trap',{timeout:60000},async t=>{
  const f=fixture(t);let calls=0;
  const trap=http.createServer((_request,response)=>{calls++;response.writeHead(200).end('unexpected');});
  await new Promise(resolve=>trap.listen(0,'127.0.0.1',resolve));t.after(()=>new Promise(resolve=>trap.close(resolve)));
  const source=fs.readFileSync(path.join(f.input,'chapter/index.html'),'utf8');
  f.write('chapter/index.html',source.replace(f.base+'image.png','http://127.0.0.1:'+trap.address().port+'/image.png'));f.seal();
  await assert.rejects(exportManual(f.options),error=>{assert.equal(error.evidence.stage,'prepare');assert.equal(error.evidence.cleanup,'passed');return true;});
  assert.equal(calls,0);assert(!fs.existsSync(f.output));
  f.write('chapter/index.html',source);
  f.write('style.css','@font-face{font-family:Trap;src:url(http://127.0.0.1:'+trap.address().port+'/font.woff)}body.pdf-book{font-family:Trap!important}');f.seal();
  await assert.rejects(exportManual(f.options),error=>error.evidence.stage==='layout');
  assert.equal(calls,0);assert(!fs.existsSync(f.output));
});

test('missing browser installation is a real installed-only refusal',{timeout:60000},async t=>{
  const f=fixture(t), browserPath=path.join(f.directory,'not-installed');
  const source='const {exportManual}=await import(process.argv[1]);try{await exportManual(JSON.parse(process.argv[2]));process.exitCode=2;}catch(error){console.log(JSON.stringify({message:error.message,evidence:error.evidence}));}';
  const result=JSON.parse(execFileSync(process.execPath,['--input-type=module','-e',source,pathToModule(),JSON.stringify({...f.options,browserPath})],{encoding:'utf8',timeout:45000,maxBuffer:1024*1024}));
  assert.equal(result.evidence.stage,'browser');assert.equal(result.evidence.cleanup,'passed');
  assert(!fs.existsSync(browserPath),'Export acquired a browser');assert(!fs.existsSync(f.output));
});

function pathToModule() {return new URL('./pdf-export.mjs',import.meta.url).href;}

test('unsafe output and changed seals do not overwrite unrelated files',{timeout:30000},async t=>{
  const f=fixture(t);fs.mkdirSync(path.dirname(f.output));fs.writeFileSync(f.output,'preserve unrelated file');
  await assert.rejects(exportManual(f.options),error=>error.evidence.stage==='inputs');
  assert.equal(fs.readFileSync(f.output,'utf8'),'preserve unrelated file');
  await assert.rejects(exportManual({...f.options,output:path.join(f.input,'manual.pdf')}),error=>error.evidence.stage==='inputs');
  assert(!fs.existsSync(path.join(f.input,'manual.pdf')));
  await assert.rejects(exportManual({...f.options,output:path.join(f.input,'new','nested','manual.pdf')}),error=>error.evidence.stage==='inputs');
  assert(!fs.existsSync(path.join(f.input,'new')),'Output refusal changed the completed build');
  const alias=path.join(f.directory,'build-alias');fs.symlinkSync(f.input,alias,'dir');
  await assert.rejects(exportManual({...f.options,output:path.join(alias,'alias-output','manual.pdf')}),error=>error.evidence.stage==='inputs');
  assert(!fs.existsSync(path.join(f.input,'alias-output')),'Output refusal followed an alias into the build');
});



test('public PDF recipes explain missing packages before running or downloading tools', t => {
  const f=fixture(t);
  for(const name of ['Makefile','go.mod'])fs.copyFileSync(path.join(root,name),path.join(f.directory,name));
  for(const target of ['docs-pdf','docs-pdf-browser-install','test-docs-pdf']) {
    assert.throws(()=>execFileSync('make',['--no-print-directory',target,'CATALOG_ENV_FILE=/dev/null'],{cwd:f.directory,encoding:'utf8',timeout:10000,stdio:'pipe'}),error=>{
      assert.notEqual(error.status,0);assert.match(error.stderr,/Run make docs-install before/);assert.doesNotMatch(error.stderr,/ERR_MODULE_NOT_FOUND/);return true;
    });
  }
  assert(!fs.existsSync(path.join(f.directory,'.tools')));
  assert(!fs.existsSync(path.join(f.directory,'documentation')));
});

test('cancellation closes an unresponsive owned browser and preserves the previous PDF', {timeout:30000,skip:process.platform==='win32'}, async t => {
  const f=fixture(t), control=await exportManual(f.options), original=fs.readFileSync(f.output);
  await resourcesClosed(control);
  let pausedPid, failure;
  t.after(()=>{
    if(pausedPid)try{process.kill(pausedPid,'SIGKILL');}catch(error){if(error.code!=='ESRCH')throw error;}
  });
  const controller=new AbortController();
  await assert.rejects(exportManual({...f.options,signal:controller.signal,onStage:(stage,state)=>{
    if(stage==='layout') {
      pausedPid=state.browserPid;
      process.kill(pausedPid,'SIGSTOP');
      setTimeout(()=>controller.abort(),10);
    }
  }}),error=>{
    failure=error;
    assert.equal(error.evidence.stage,'layout');
    assert.match(error.message,/interrupted/);
    return true;
  });
  await resourcesClosed(failure.evidence);
  pausedPid=undefined;
  assert.deepEqual(fs.readFileSync(f.output),original);
});
