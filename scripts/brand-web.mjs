// Puts Greengram's name on a built Web A: the manifests, index.html's title,
// description and links, and nothing left pointing at web.telegram.org.
// build-web.sh copies the icons first and runs this:
//
import fs from 'node:fs';
import path from 'node:path';

const NAME = 'Greengram';
const DESCRIPTION = 'Greengram is a self-hosted messenger. Not affiliated with Telegram.';

const dir = process.argv[2];
if (!dir) {
  console.error('usage: bun brand-web.mjs <web dir>');
  process.exit(2);
}

for (const file of fs.readdirSync(dir).filter((f) => f.endsWith('.webmanifest'))) {
  const p = path.join(dir, file);
  const m = JSON.parse(fs.readFileSync(p, 'utf8'));
  m.name = NAME;
  m.short_name = NAME;
  m.description = DESCRIPTION;
  // Web A's store screenshot is of Telegram
  delete m.screenshots;
  fs.writeFileSync(p, JSON.stringify(m, null, 2) + '\n');
}
fs.rmSync(path.join(dir, 'screenshot.jpg'), { force: true });

const indexPath = path.join(dir, 'index.html');
let html = fs.readFileSync(indexPath, 'utf8');
const escaped = DESCRIPTION.replace(/&/g, '&amp;').replace(/"/g, '&quot;');
html = html
  .replace(/<title>[^<]*<\/title>/, `<title>${NAME}</title>`)
  .replace(/(<meta\s+(?:name|property)="(?:description|og:description|twitter:description)"\s+content=")[^"]*(")/g, `$1${escaped}$2`)
  .replace(/\s*<meta\s+property="(?:og|twitter):url"[^>]*>/g, '')
  .replace(/\s*<link\s+rel="canonical"[^>]*>/g, '')
  .replace(/<h1>Telegram Web<\/h1>/g, `<h1>${NAME}</h1>`)
  .replace(/(<meta\s+(?:name|property)="[^"]*(?:title|application-name)"\s+content=")Telegram[^"]*(")/g, `$1${NAME}$2`);
fs.writeFileSync(indexPath, html);

// what must not survive; a new Web A that words these differently fails here
// instead of shipping Telegram's name
const left = [];
for (const file of ['index.html', ...fs.readdirSync(dir).filter((f) => f.endsWith('.webmanifest'))]) {
  const text = fs.readFileSync(path.join(dir, file), 'utf8');
  for (const bad of ['Telegram Web', 'Telegram is a cloud-based', 'web.telegram.org/a', 'screenshot.jpg']) {
    if (text.includes(bad)) left.push(`${file}: ${bad}`);
  }
}
if (left.length) {
  console.error('still branded Telegram:\n  ' + left.join('\n  '));
  process.exit(1);
}
console.log(`branded ${dir}`);
