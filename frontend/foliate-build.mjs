import { readFile } from 'node:fs/promises';

export const foliateReader = {
    name: 'foliate-reader',
    setup(build) {
        // Add cancellable resource loading and iframe reuse to the paginator.
        build.onLoad({ filter: /[/\\]foliate-js[/\\]paginator\.js$/ }, async ({ path }) => {
            let contents = await readFile(path, 'utf8');
            for (const [before, after] of [
                // Paragraph CFIs may start with a hidden footnote/bookmark anchor.
                [
                    'if (node?.nodeType === 1) return node',
                    'if (node?.nodeType === 1 && node.getClientRects().length) return node',
                ],
                [
                    'return new Promise(resolve => {\n            this.#iframe',
                    'return new Promise((resolve, reject) => {\n            const signal = this.container.loadSignal\n            this.#iframe',
                ],
                ["this.#iframe.addEventListener('load', () => {", 'const loaded = async () => {'],
                [
                    'const doc = this.document\n                afterLoad?.(doc)',
                    'const doc = this.document\n                await waitForImages(doc, signal)\n                afterLoad?.(doc)',
                ],
                [
                    'doc.fonts.ready.then(() => this.expand())',
                    'waitForFonts(doc, signal).then(() => this.expand(), () => {})',
                ],
                // Wait for fonts to avoid a second expensive column layout in WebKit.
                [
                    "this.#iframe.style.display = 'block'\n                this.render(layout)",
                    "this.#iframe.style.display = 'block'\n                if (this.container.loadDocument) await waitForFonts(doc, signal)\n                this.render(layout)",
                ],
                [
                    'this.#view?.document?.fonts?.ready?.then(() => this.#view.expand())',
                    'if (this.#view?.document) waitForFonts(this.#view.document, this.loadSignal).then(() => this.#view?.expand(), () => {})',
                ],
                [
                    '}, { once: true })\n            this.#iframe.src = src',
                    `}
            if (this.container.loadDocument)
                this.container.loadDocument(this.#iframe, src, signal).then(loaded).catch(reject)
            else {
                this.#iframe.addEventListener('load', () => loaded().catch(reject), { once: true })
                this.#iframe.src = src
            }`,
                ],
                [
                    'if (this.document) this.#observer.unobserve(this.document.body)',
                    'this.#observer.disconnect()',
                ],
                [
                    'this.#container.removeChild(this.#view.element)',
                    `if (this.loadDocument) {
                this.#view.overlayer?.element.remove()
                return this.#view
            }
            this.#container.removeChild(this.#view.element)`,
                ],
                // A cancelled measurement may close before its first chapter.
                [
                    'this.#observer.unobserve(this)\n        this.#view.destroy()',
                    'this.#observer.disconnect()\n        this.#view?.destroy()',
                ],
            ]) {
                contents = replaceRequired(contents, before, after);
            }
            return {
                contents: `import { waitForFonts, waitForImages } from '../../frontend/src/reader/foliate-resources.ts';\n${contents}`,
                loader: 'js',
            };
        });
        build.onLoad({ filter: /[/\\]foliate-js[/\\]fixed-layout\.js$/ }, async ({ path }) => {
            let contents = await readFile(path, 'utf8');
            // Fix Foliate 1.0.1's `side` typo and report navigation within a spread.
            // The selected side must also survive a later resize.
            for (const [before, after] of [
                ["this.side === 'left'", "this.#side === 'left'"],
                [
                    'if (!side) return\n        const left',
                    'if (!side) return\n        this.#side = side\n        const left',
                ],
                [
                    'this.#render(side)\n            return',
                    'this.#render(side)\n            this.#reportLocation(reason)\n            return',
                ],
            ]) {
                contents = replaceRequired(contents, before, after);
            }
            return { contents, loader: 'js' };
        });
    },
};

function replaceRequired(contents, before, after) {
    if (contents.split(before).length !== 2) {
        throw new Error(`Foliate changed; review reader adaptation: ${before}`);
    }
    return contents.replace(before, after);
}
