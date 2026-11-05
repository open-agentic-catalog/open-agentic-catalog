import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {notices, browserSuppliers} from './notices.mjs';

function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(),'catalog-notices-'));
  t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
  const write = (file,text)=>{
    const target = path.join(directory,file);
    fs.mkdirSync(path.dirname(target),{recursive:true});
    fs.writeFileSync(target,text);
  };
  const packages = {};
  for (const name of browserSuppliers) {
    packages['node_modules/' + name] = {version:name.includes('pagefind')?'1.5.2':'1.0.0',license:'MIT'};
    fs.mkdirSync(path.join(directory,'node_modules',name),{recursive:true});
    if (!['@pagefind/default-ui','pagefind'].includes(name)) write('node_modules/' + name + '/LICENSE','License for ' + name + '\n');
  }
  write('node_modules/pagefind/LICENSE/LICENSE','Pagefind primary license\n');
  write('node_modules/pagefind/LICENSE/LICENSE-vscode-ripgrep','Microsoft companion notice\n');
  write('package-lock.json',JSON.stringify({packages}));
  write('scripts/licenses/pagefind-default-ui-1.5.2.txt','Pinned Pagefind repository license\n');
  write('src/assets/navigation/LICENSE','Navigation icon attribution\n');
  fs.mkdirSync(path.join(directory,'public/generated'),{recursive:true});
  return {directory,write,packages};
}

test('notices retain nested Pagefind texts, pinned default UI text and icon attribution',t=>{
  const {directory} = fixture(t);
  notices(directory);
  const text = fs.readFileSync(path.join(directory,'public/generated/third-party-notices.txt'),'utf8');
  for (const required of ['Pagefind primary license\n','Microsoft companion notice\n','Pinned Pagefind repository license\n','Navigation icon attribution\n']) assert.ok(text.includes(required),required);
  const suppliers = JSON.parse(fs.readFileSync(path.join(directory,'public/generated/browser-inventory.json')));
  assert.deepEqual(suppliers.packages.find(p=>p.name==='pagefind').notices,['LICENSE/LICENSE','LICENSE/LICENSE-vscode-ripgrep']);
  assert.deepEqual(suppliers.packages.find(p=>p.name==='@pagefind/default-ui').notices,['scripts/licenses/pagefind-default-ui-1.5.2.txt']);
});

test('every browser supplier must be installed with a nonempty notice',t=>{
  const {directory} = fixture(t);
  for (const name of browserSuppliers.filter(n=>n!=='@pagefind/default-ui')) {
    for (const relative of name==='pagefind'?['LICENSE/LICENSE','LICENSE/LICENSE-vscode-ripgrep']:['LICENSE']) {
      const file = path.join(directory,'node_modules',name,relative);
      const original = fs.readFileSync(file);
      fs.unlinkSync(file);
      assert.throws(()=>notices(directory),/Missing browser supplier notice/);
      fs.writeFileSync(file,' \n');
      assert.throws(()=>notices(directory),/Empty license text/);
      fs.writeFileSync(file,original);
    }
  }
  fs.rmSync(path.join(directory,'node_modules/@pagefind/default-ui'),{recursive:true});
  assert.throws(()=>notices(directory),/Missing browser supplier notice/);
});

test('missing, empty or stale upstream fallback prevents generation',t=>{
  const {directory,write,packages} = fixture(t);
  const file = path.join(directory,'scripts/licenses/pagefind-default-ui-1.5.2.txt');
  fs.unlinkSync(file);
  assert.throws(()=>notices(directory),/ENOENT/);
  write('scripts/licenses/pagefind-default-ui-1.5.2.txt','\n');
  assert.throws(()=>notices(directory),/Empty upstream notice/);
  write('scripts/licenses/pagefind-default-ui-1.5.2.txt','Reviewed upstream text\n');
  packages['node_modules/@pagefind/default-ui'].version = '1.5.3';
  write('package-lock.json',JSON.stringify({packages}));
  assert.throws(()=>notices(directory),/Review upstream notice/);
});

test('unavailable optional build tools remain inventory-only entries',t=>{
  const {directory,write,packages} = fixture(t);
  packages['node_modules/optional-build-tool'] = {version:'1.0.0',license:'MIT',optional:true};
  write('package-lock.json',JSON.stringify({packages}));
  notices(directory);
  const inventory = JSON.parse(fs.readFileSync(path.join(directory,'public/generated/dependency-inventory.json')));
  assert.equal(inventory.find(p=>p.name==='optional-build-tool').optional,true);
  const text = fs.readFileSync(path.join(directory,'public/generated/third-party-notices.txt'),'utf8');
  assert.ok(!text.includes('optional-build-tool'));
});

test('unsafe nested notices and empty icon attribution fail generation',t=>{
  const {directory,write} = fixture(t);
  const license = path.join(directory,'node_modules/pagefind/LICENSE');
  fs.rmSync(license,{recursive:true});
  fs.mkdirSync(license);
  fs.symlinkSync(path.join(directory,'package-lock.json'),path.join(license,'linked'));
  assert.throws(()=>notices(directory),/Unsafe license file/);
  fs.rmSync(license,{recursive:true});
  write('node_modules/pagefind/LICENSE/LICENSE','Pagefind license\n');
  write('node_modules/pagefind/LICENSE/LICENSE-vscode-ripgrep','Companion license\n');
  write('src/assets/navigation/LICENSE',' \n');
  assert.throws(()=>notices(directory),/Missing navigation icon license/);
});
