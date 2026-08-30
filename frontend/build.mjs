// Bundle the frontend into internal/web/static, where go:embed picks it up.
// Run from the repo root (`npm run build`).

import { copyFile, cp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';
import * as esbuild from 'esbuild';

const staticRoot = 'internal/web/static';

const common = {
    bundle: true,
    minify: true,
    target: ['es2018'],
    logLevel: 'warning',
    write: true,
};

await esbuild.build({
    ...common,
    entryPoints: ['frontend/src/main.ts'],
    outfile: `${staticRoot}/app.js`,
});

await esbuild.build({
    ...common,
    entryPoints: ['frontend/src/reader/index.ts'],
    outfile: `${staticRoot}/reader.js`,
});

await esbuild.build({
    ...common,
    entryPoints: ['frontend/src/reader/pdf-index.ts'],
    outfile: `${staticRoot}/pdf-reader.js`,
});

await esbuild.build({
    ...common,
    entryPoints: ['node_modules/pdfjs-dist/legacy/build/pdf.worker.mjs'],
    outfile: `${staticRoot}/pdf.worker.js`,
});

await esbuild.build({
    ...common,
    entryPoints: ['frontend/src/styles/style.css'],
    outfile: `${staticRoot}/style.css`,
});

// PDF.js loads CMaps, color profiles, standard fonts, and image decoders on
// demand. Keep those version-matched with the pinned package, including their
// own license files, and let go:embed absorb the generated directory.
const pdfResourceRoot = `${staticRoot}/pdfjs`;
await rm(pdfResourceRoot, { recursive: true, force: true });
await mkdir(pdfResourceRoot, { recursive: true });
for (const directory of ['cmaps', 'iccs', 'standard_fonts', 'wasm']) {
    await cp(`node_modules/pdfjs-dist/${directory}`, `${pdfResourceRoot}/${directory}`, {
        recursive: true,
    });
}

// Keep the distribution self-contained by embedding its license and the
// canonical third-party notice file.
await rm(`${staticRoot}/licenses`, { recursive: true, force: true });
await copyFile('LICENSE', `${staticRoot}/LICENSE.txt`);
await copyFile('ThirdPartyNotices.txt', `${staticRoot}/ThirdPartyNotices.txt`);

// Precompress the assets a browser pulls on the browse and reading paths, so a
// plain install without a compressing reverse proxy still gets the small wire
// size. The server picks the `.gz` sibling when the client accepts gzip and
// falls back to the original otherwise, so a missing sibling is never an error.
// Only text-like and wasm payloads qualify: the pdf.js cmaps and bundled fonts
// barely shrink and would add hundreds of files to the embedded tree for a few
// percent.
const compressibleExtensions = ['.js', '.css', '.svg', '.webmanifest', '.json', '.wasm'];
const minCompressionSaving = 0.15;

const staticFiles = (await readdir(staticRoot, { recursive: true, withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .map((entry) => join(entry.parentPath, entry.name));

// Sweep first: an asset that stopped qualifying, or disappeared entirely, must
// not leave a stale `.gz` behind for go:embed to pick up.
for (const file of staticFiles.filter((file) => file.endsWith('.gz'))) {
    await rm(file);
}

for (const file of staticFiles) {
    if (file.endsWith('.gz')) continue;
    if (!compressibleExtensions.some((ext) => file.endsWith(ext))) continue;

    const raw = await readFile(file);
    const compressed = gzipSync(raw, { level: 9 });
    if (compressed.length > raw.length * (1 - minCompressionSaving)) continue;
    await writeFile(`${file}.gz`, compressed);
}
