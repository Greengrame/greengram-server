// Bundles the panel into <out dir> (default dist/admin-assets): main.js and
// main.css, which admin.html loads from /admin-assets/. scripts/build-admin.sh
// in the repository root runs this and copies admin.html next to it.
import tailwind from 'bun-plugin-tailwind';
import { resolve } from 'node:path';

const root = import.meta.dir;
const outdir = resolve(process.argv[2] || resolve(root, 'dist/admin-assets'));
const result = await Bun.build({
    entrypoints: [resolve(root, 'src/main.jsx')],
    outdir,
    naming: '[name].[ext]',
    target: 'browser',
    minify: true,
    plugins: [tailwind],
    define: { 'process.env.NODE_ENV': '"production"' }
});
if (!result.success) throw new AggregateError(result.logs, 'Admin build failed.');
console.log(result.outputs.map(output => output.path).join('\n'));
