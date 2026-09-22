// Polka uses its own PDF.js reader. Exclude Foliate's optional PDF adapter,
// whose top-level await cannot be included in the reader's classic script.
export const foliateReader = {
    name: 'foliate-reader',
    setup(build) {
        build.onResolve({ filter: /^\.\/pdf\.js$/ }, ({ importer }) => {
            if (/[/\\]foliate-js[/\\]view\.js$/.test(importer))
                return { path: 'pdf', namespace: 'unused-foliate-pdf' };
        });
        build.onLoad({ filter: /.*/, namespace: 'unused-foliate-pdf' }, () => ({
            contents: 'export const makePDF = () => null',
            loader: 'js',
        }));
    },
};
