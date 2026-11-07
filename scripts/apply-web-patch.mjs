import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const patchFile = process.argv[2];
const sourceDirectory = process.argv[3];
const sections = readFileSync(patchFile, 'utf8').split(/(?=^diff --git )/m).filter(Boolean);

function runPatch(section, flags) {
  const result = spawnSync('patch', ['-p1', '--batch', '--quiet', ...flags], {
    cwd: sourceDirectory,
    input: section,
    encoding: 'utf8',
  });
  if (result.error) throw result.error;
  return result;
}

function applySection(section) {
  const forward = runPatch(section, ['--forward', '--dry-run']);
  let reverse;
  let applied;
  if (forward.status === 0) {
    applied = runPatch(section, ['--forward']);
    if (applied.status === 0) return;
    throw new Error(applied.stderr || applied.stdout);
  }
  reverse = runPatch(section, ['--reverse', '--dry-run']);
  if (reverse.status === 0) return;
  throw new Error(forward.stderr || forward.stdout);
}

for (const section of sections) applySection(section);
