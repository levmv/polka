import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';

// Only the binary stays outside Git. Warm builds need no Go process or network.
export async function prepareDjvuWASM() {
    const root = new URL('../internal/format/djvu/', import.meta.url);
    const spec = JSON.parse(await readFile(new URL('djvutang.json', root), 'utf8'));
    const wasm = await readFile(new URL('generated/djvutang.wasm', root)).catch((error) => {
        if (error.code !== 'ENOENT') throw error;
        return null;
    });
    if (wasm && createHash('sha256').update(wasm).digest('hex') === spec.wasm_sha256) return;
    execFileSync('go', ['run', './internal/format/djvu/wasmtool'], {
        stdio: 'inherit',
        env: { ...process.env, GOOS: '', GOARCH: '' },
    });
}
