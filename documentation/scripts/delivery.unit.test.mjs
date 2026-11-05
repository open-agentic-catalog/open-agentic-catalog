import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {bundle} from './bundle.mjs';
import {deliveryInventory,stageDelivery} from './delivery.mjs';
import {edition} from './edition.mjs';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');

test('Public editions require a safe origin and exact revision; ordinary builds stay optional', () => {
  assert.equal(edition({}),null);
  const commit = 'a'.repeat(40);
  assert.equal(edition({DOCS_SITE:'https://example.test',DOCS_EDITION_COMMIT:commit}).commit,commit);
  for (const site of ['http://example.test','https://user:pass@example.test','https://example.test/sub/','https://example.test/?token=x'])
    assert.throws(()=>edition({DOCS_SITE:site,DOCS_EDITION_COMMIT:commit}));
  assert.throws(()=>edition({DOCS_SITE:'https://example.test',DOCS_EDITION_COMMIT:'short'}));
  assert.throws(()=>edition({DOCS_EDITION_COMMIT:commit}));
});

test('Delivery inventories the exact site, PDF and evidence without modifying the sealed build', t => {
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'manual-delivery-'));
  try {
    const input=path.join(dir,'site'),pdf=path.join(dir,'manual.pdf'),output=path.join(dir,'edition');
    fs.mkdirSync(input);
    const value={site:'https://example.test',version:'0.1.0',commit:'a'.repeat(40)};
    const html=`<html><a href="${value.site}/catalog/catalog-manual.pdf">PDF</a><p>Version ${value.version}. Source ${value.commit}</p></html>`;
    fs.writeFileSync(path.join(input,'index.html'),html);
    const lockBytes=Buffer.from('lock');
    const manifest=JSON.stringify(bundle(input,'/catalog/',lockBytes))+'\n';
    fs.writeFileSync(path.join(input,'documentation-bundle.json'),manifest);
    fs.writeFileSync(pdf,'%PDF-'+'.'.repeat(200));
    const exportResult={status:'complete',cleanup:'passed',manifestSha256:sha(manifest),base:'/catalog/',applicationVersion:value.version,pdfBytes:205,pdfSha256:sha(fs.readFileSync(pdf))};
    const inputs={input,pdf,output,value,lockBytes,exportResult,expectedBase:'/catalog/'};
    const result=stageDelivery(inputs);
    assert.deepEqual(result.files.map(f=>f.path),['catalog-manual.pdf','documentation-bundle.json','index.html']);
    assert.equal(fs.readFileSync(path.join(input,'documentation-bundle.json'),'utf8'),manifest);
    assert.equal(fs.readFileSync(path.join(input,'index.html'),'utf8'),html);
    assert.deepEqual(deliveryInventory(output),result.files);
    assert.throws(()=>stageDelivery(inputs),/already exists/);
    for (const [name,change] of Object.entries({
      missing:undefined,failed:{...exportResult,status:'failed'},cleanup:{...exportResult,cleanup:'failed'},
      staleManifest:{...exportResult,manifestSha256:'0'.repeat(64)},
      wrongVersion:{...exportResult,applicationVersion:'different'},wrongBase:{...exportResult,base:'/other/'},
      changedPDF:{...exportResult,pdfSha256:'0'.repeat(64)},wrongSize:{...exportResult,pdfBytes:204},
    })) {
      const refused=path.join(dir,'refused-'+name);
      assert.throws(()=>stageDelivery({...inputs,output:refused,exportResult:change}),/PDF/);
      assert.equal(fs.existsSync(refused),false);
    }
    for (const [name,change] of Object.entries({
      site:{value:{...value,site:'https://different.test'}},
      base:{expectedBase:'/other/'},
      version:{value:{...value,version:'0.2.0'}},
      commit:{value:{...value,commit:'b'.repeat(40)}},
    })) {
      const refused=path.join(dir,'refused-'+name);
      assert.throws(()=>stageDelivery({...inputs,...change,output:refused}),/public edition/);
      assert.equal(fs.existsSync(refused),false);
    }
    // A newly sealed manual cannot reuse the successful export of its predecessor.
    fs.writeFileSync(path.join(input,'index.html'),html+'<p>New content</p>');
    fs.writeFileSync(path.join(input,'documentation-bundle.json'),JSON.stringify(bundle(input,'/catalog/',lockBytes))+'\n');
    assert.throws(()=>stageDelivery({...inputs,output:path.join(dir,'changed-build')}),/PDF export does not match/);
    fs.writeFileSync(path.join(input,'index.html'),html);
    fs.writeFileSync(path.join(input,'documentation-bundle.json'),manifest);
    const copyFile=fs.copyFileSync;
    const corruptCopy=t.mock.method(fs,'copyFileSync',(source,destination,...options)=>{
      copyFile(source,destination,...options);
      if (path.basename(destination)==='catalog-manual.pdf') fs.writeFileSync(destination,'%PDF-'+ '!'.repeat(200));
    });
    try {
      const corruptOutput=path.join(dir,'corrupt-copy');
      assert.throws(()=>stageDelivery({...inputs,output:corruptOutput}),/PDF changed during staging/);
      assert.equal(fs.existsSync(corruptOutput),false);
      assert.equal(fs.readdirSync(dir).some(name=>name.startsWith('corrupt-copy.stage-')),false);
      assert.equal(sha(fs.readFileSync(pdf)),exportResult.pdfSha256,'The original PDF remains unchanged');
    } finally {corruptCopy.mock.restore();}
    const corruptManifest=t.mock.method(fs,'copyFileSync',(source,destination,...options)=>{
      copyFile(source,destination,...options);
      if (path.basename(destination)==='documentation-bundle.json') fs.writeFileSync(destination,'{}\n');
    });
    try {
      const corruptOutput=path.join(dir,'corrupt-manifest');
      assert.throws(()=>stageDelivery({...inputs,output:corruptOutput}),/Manual manifest changed during staging/);
      assert.equal(fs.existsSync(corruptOutput),false);
      assert.equal(fs.readdirSync(dir).some(name=>name.startsWith('corrupt-manifest.stage-')),false);
    } finally {corruptManifest.mock.restore();}
    fs.writeFileSync(pdf,'failed');
    assert.throws(()=>stageDelivery({...inputs,output:path.join(dir,'failed')}),/PDF/);
    assert.equal(fs.existsSync(path.join(dir,'failed')),false);
    fs.symlinkSync(pdf,path.join(output,'redirect'));
    assert.throws(()=>deliveryInventory(output),/Non-regular/);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});

test('Ordinary bundle limits stay strict; only the final delivery allows a larger PDF', () => {
  const dir=fs.mkdtempSync(path.join(os.tmpdir(),'manual-bounds-'));
  try {
    fs.writeFileSync(path.join(dir,'index.html'),'<html></html>');
    const fd=fs.openSync(path.join(dir,'catalog-manual.pdf'),'w');
    fs.ftruncateSync(fd,33*1024*1024);fs.closeSync(fd);
    assert.throws(()=>bundle(dir,'/',Buffer.from('lock')),/bounds/);
    assert.equal(deliveryInventory(dir).length,2);
    fs.renameSync(path.join(dir,'catalog-manual.pdf'),path.join(dir,'other.pdf'));
    assert.throws(()=>deliveryInventory(dir),/bounds/);
  } finally {fs.rmSync(dir,{recursive:true,force:true});}
});
