#!/usr/bin/env node
// Run after every new wallet capture, with no dependencies:
//   node scripts/walt-fixtures.mjs <greengram-web>/vendor/walt
// Checks the capture against the lock, every patch anchor against the pinned
// files, and every module, export and chunk the adapter names. On success it
// rewrites internal/miniapps/testdata, which the Go tests read: the capture
// lives in another repo and they can't see it.
import { createHash } from 'node:crypto';
import { gzipSync } from 'node:zlib';
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = resolve(dirname(fileURLToPath(import.meta.url)), '../internal/miniapps');
const vendor = process.argv[2];
if (!vendor) { console.error('usage: node scripts/walt-fixtures.mjs <vendor/walt>'); process.exit(2); }
const lock = JSON.parse(readFileSync(join(here, 'locks/walt.lock.json'), 'utf8'));
const patches = JSON.parse(readFileSync(join(here, 'walt_patches.json'), 'utf8'));
const problems = [];
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const read = key => readFileSync(join(vendor, key));

for (const [key, asset] of Object.entries(lock.assets)) {
    let bytes;
    try { bytes = read(key); } catch { problems.push(`capture is missing ${key}`); continue; }
    if (sha(bytes) !== asset.sha256) problems.push(`capture differs from the lock: ${key}`);
}

for (const patch of patches) {
    if (!lock.assets[patch.file]) { problems.push(`${patch.name}: ${patch.file} is not in the lock`); continue; }
    let text = read(patch.file).toString('utf8');
    for (const edit of patch.edits) {
        const want = edit.count || 1;
        const got = text.split(edit.old).length - 1;
        if (got !== want) problems.push(`${patch.name}: ${got} matches of ${JSON.stringify(edit.old.slice(0, 80))}, want ${want}`);
        text = text.split(edit.old).join(edit.new);
    }
}

// the module tables of every chunk, without running any of them
const modules = new Map();
const moduleHead = /(?:__webpack_esm_modules__=\{|[,}])(\d{1,6})\(([\w$]*)(?:,([\w$]+))?(?:,([\w$]+))?\)\{/g;
const scripts = Object.keys(lock.assets).filter(key => /^static\/js\/.*\.js$/.test(key));
for (const key of scripts) {
    const text = read(key).toString('utf8');
    if (!text.includes('__webpack_esm_modules__')) continue;
    const heads = [...text.matchAll(moduleHead)];
    heads.forEach((head, index) => {
        const body = text.slice(head.index, heads[index + 1]?.index ?? text.length);
        const entry = modules.get(head[1]) || { exports: new Set(), declared: false };
        for (const list of body.matchAll(/\.d\([\w$]+,\{((?:[\w$]+:\(\)=>[\w$.]+,?)+)\}/g)) {
            entry.declared = true;
            list[1].split(',').forEach(item => entry.exports.add(item.split(':')[0]));
        }
        for (const list of body.matchAll(/\.d\([\w$]+,\[/g)) {
            entry.declared = true;
            for (const name of body.slice(list.index, list.index + 200000).matchAll(/(?:\[|,)"([\w$]+)",(?:0|\(\)=>|function)/g)) entry.exports.add(name[1]);
        }
        modules.set(head[1], entry);
    });
}

const runtimeKey = Object.keys(lock.assets).find(key => /^static\/js\/runtime\.[a-f0-9]+\.js$/.test(key));
const runtime = read(runtimeKey).toString('utf8');
const map = runtime.slice(runtime.indexOf('.u='), runtime.indexOf('.miniCssF='));
const chunks = new Set([...map.matchAll(/(?:\{|,)(\d+):"/g)].map(match => match[1]));

// same scan as TestWaltModuleRefs: keep the two in step
const refs = [];
const used = new Set();
const staticDir = join(here, 'static');
for (const file of readdirSync(staticDir).filter(name => /^(?:walt-|wallet-).*\.js$/.test(name))) {
    const text = readFileSync(join(staticDir, file), 'utf8');
    for (const match of text.matchAll(/requireModule\((\d+)\)(?:\.([\w$]+))?/g)) refs.push({ file, id: match[1], name: match[2] });
    for (const match of text.matchAll(/requireModule\.m\[(\d+)\]/g)) refs.push({ file, id: match[1] });
    for (const match of text.matchAll(/decorateModule\((\d+),[^{;]*\{\s*(?:let [\w$]+ = )?(?:exports|descriptors)\.([\w$]+)/g)) refs.push({ file, id: match[1], name: match[2] });
    for (const match of text.matchAll(/decorateModule\((\d+),/g)) refs.push({ file, id: match[1] });
    for (const list of text.matchAll(/\[([\d, ]+)\]\.map\(id => requireModule\.e\(id\)\)/g)) {
        for (const id of list[1].split(',').map(item => item.trim())) {
            if (!chunks.has(id)) problems.push(`${file}: chunk ${id} is not in the runtime`);
            used.add(id);
        }
    }
}
const index = {};
for (const { file, id, name } of refs) {
    const entry = modules.get(id);
    if (!entry) { problems.push(`${file}: module ${id} is not in the capture`); continue; }
    if (name && entry.declared && !entry.exports.has(name)) problems.push(`${file}: module ${id} has no export ${name}`);
    index[id] = entry.declared ? [...entry.exports].sort() : null;
}

if (problems.length) {
    console.error(problems.join('\n'));
    console.error(`\n${problems.length} problems, testdata left alone.`);
    process.exit(1);
}

const testdata = join(here, 'testdata');
rmSync(testdata, { recursive: true, force: true });
mkdirSync(join(testdata, 'walt'), { recursive: true });
for (const patch of patches) writeFileSync(join(testdata, 'walt', basename(patch.file) + '.gz'), gzipSync(read(patch.file), { level: 9 }));
const sorted = Object.fromEntries(Object.keys(index).sort((a, b) => a - b).map(id => [id, index[id]]));
writeFileSync(join(testdata, 'walt-modules.json'), JSON.stringify({ version: lock.version, chunks: [...used].sort((a, b) => a - b), modules: sorted }, null, 1) + '\n');
console.log(`${lock.version}: ${patches.length} patches and ${Object.keys(index).length} modules fit, testdata written.`);
