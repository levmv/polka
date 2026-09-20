// Bundle the frontend into internal/web/static/generated for go:embed.
// Run from the repo root (`npm run build`).

import { createHash } from 'node:crypto';
import { copyFile, cp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { basename, join } from 'node:path';
import { gzipSync } from 'node:zlib';
import * as esbuild from 'esbuild';
import { prepareDjvuWASM } from './djvu-build.mjs';
import { foliateReader } from './foliate-build.mjs';

const staticRoot = 'internal/web/static/generated';

await prepareDjvuWASM();
await rm(staticRoot, { recursive: true, force: true });
await cp('frontend/static', staticRoot, { recursive: true });

const pdfFontNames = ['Regular', 'Bold', 'Italic', 'BoldItalic'].map(
    (style) => `LiberationSans-${style}`,
);

// The reader uses browser font substitution with XFA disabled, so its fallback
// fonts can be WOFF2. Keep this adaptation here without modifying node_modules.
const pdfFonts = {
    name: 'pdf-fonts',
    setup(build) {
        build.onLoad({ filter: /[/\\]pdf\.worker\.mjs$/ }, async ({ path }) => {
            let contents = await readFile(path, 'utf8');
            for (const name of pdfFontNames) {
                if (!contents.includes(`${name}.ttf`)) {
                    throw new Error(`PDF.js no longer references ${name}.ttf; review font loading`);
                }
                contents = contents.replaceAll(`${name}.ttf`, `${name}.woff2`);
            }
            return { contents, loader: 'js' };
        });
    },
};

const common = {
    bundle: true,
    minify: true,
    target: ['es2018'],
    logLevel: 'warning',
    write: true,
    plugins: [pdfFonts, foliateReader],
};

for (const [entry, name, allowedEngine] of [
    ['frontend/src/main.ts', 'app.js', null],
    ['frontend/src/reader/index.ts', 'reader.js', 'foliate-js'],
    ['frontend/src/reader/pdf-index.ts', 'pdf-reader.js', 'pdfjs-dist'],
    ['frontend/src/reader/djvu-index.ts', 'djvu-reader.js', 'djvutang'],
    ['internal/format/djvu/vendor/worker.mjs', 'djvu.worker.js', 'djvutang'],
    ['node_modules/pdfjs-dist/legacy/build/pdf.worker.mjs', 'pdf.worker.js', 'pdfjs-dist'],
]) {
    const { metafile } = await esbuild.build({
        ...common,
        entryPoints: [entry],
        outfile: `${staticRoot}/${name}`,
        format: allowedEngine === 'djvutang' ? 'esm' : 'iife',
        supported: { 'import-meta': true },
        metafile: true,
    });
    // Shared UI must not pull a reader engine into the app or the other reader.
    // Check bundled contributions, so erased type imports remain harmless.
    for (const output of Object.values(metafile.outputs)) {
        for (const [path, input] of Object.entries(output.inputs)) {
            const engine = path.includes('internal/format/djvu/vendor/')
                ? 'djvutang'
                : path.match(/node_modules\/(foliate-js|pdfjs-dist)\//)?.[1];
            if (input.bytesInOutput > 0 && engine && engine !== allowedEngine) {
                throw new Error(`${name} unexpectedly bundles ${engine} via ${path}`);
            }
        }
    }
}

await copyFile('internal/format/djvu/generated/djvutang.wasm', `${staticRoot}/djvutang.wasm`);

await esbuild.build({
    ...common,
    entryPoints: ['frontend/src/styles/style.css'],
    outfile: `${staticRoot}/style.css`,
    // Keep explicit edge offsets; minification would otherwise introduce inset.
    supported: { 'inset-property': false },
});

// Invalidate saved screen counts whenever the reader or its styles change.
const readerPath = `${staticRoot}/reader.js`;
const readerCode = await readFile(readerPath, 'utf8');
const readerLayoutVersion = createHash('sha256')
    .update(readerCode)
    .update(await readFile(`${staticRoot}/style.css`))
    .digest('hex');
await writeFile(
    readerPath,
    `const POLKA_READER_LAYOUT_VERSION = "${readerLayoutVersion}";\n${readerCode}`,
);

// PDF.js loads CMaps, color profiles, standard fonts, and image decoders on
// demand. Keep package resources version-matched, replacing its Liberation 1.x
// fonts with our OFL-licensed WOFF2 files. See fonts/README.md for provenance.
const pdfResourceRoot = `${staticRoot}/pdfjs`;
const replacedFonts = new Set([...pdfFontNames.map((name) => `${name}.ttf`), 'LICENSE_LIBERATION']);
await mkdir(pdfResourceRoot, { recursive: true });
for (const directory of ['cmaps', 'iccs', 'standard_fonts', 'wasm']) {
    await cp(`node_modules/pdfjs-dist/${directory}`, `${pdfResourceRoot}/${directory}`, {
        recursive: true,
        filter: (source) => !replacedFonts.has(basename(source)),
    });
}
for (const name of pdfFontNames) {
    await copyFile(
        `frontend/fonts/${name}.woff2`,
        `${pdfResourceRoot}/standard_fonts/${name}.woff2`,
    );
}
await copyFile('frontend/fonts/OFL.txt', `${pdfResourceRoot}/standard_fonts/LICENSE_LIBERATION`);

// Keep the distribution self-contained by embedding its license and the
// canonical third-party notice file.
await copyFile('LICENSE', `${staticRoot}/LICENSE.txt`);
await copyFile('ThirdPartyNotices.txt', `${staticRoot}/ThirdPartyNotices.txt`);

// Precompress the assets a browser pulls on the browse and reading paths, so a
// plain install without a compressing reverse proxy still gets the small wire
// size. The server picks the `.gz` sibling when the client accepts gzip and
// falls back to the original otherwise, so a missing sibling is never an error.
// Only text-like and wasm payloads qualify. WOFF2 is already compressed; CMaps
// and the remaining font resources are kept as supplied by PDF.js.
const compressibleExtensions = ['.js', '.css', '.svg', '.webmanifest', '.json', '.wasm'];
const minCompressionSaving = 0.15;

const staticFiles = (await readdir(staticRoot, { recursive: true, withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .map((entry) => join(entry.parentPath, entry.name));

for (const file of staticFiles) {
    if (!compressibleExtensions.some((ext) => file.endsWith(ext))) continue;

    const raw = await readFile(file);
    const compressed = gzipSync(raw, { level: 9 });
    if (compressed.length > raw.length * (1 - minCompressionSaving)) continue;
    await writeFile(`${file}.gz`, compressed);
}
