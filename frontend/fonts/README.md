# PDF fallback fonts

These four files are the complete Liberation Sans 2.1.5 fonts, compressed to
WOFF2 without subsetting or removing hinting. They are licensed under the
[SIL Open Font License 1.1](OFL.txt).

Upstream: [Liberation Fonts 2.1.5](https://github.com/liberationfonts/liberation-fonts/releases/tag/2.1.5).
The original TTF archive is
[liberation-fonts-ttf-2.1.5.tar.gz](https://github.com/liberationfonts/liberation-fonts/files/7261482/liberation-fonts-ttf-2.1.5.tar.gz),
with SHA-256:

```text
7191c669bf38899f73a2094ed00f7b800553364f90e2637010a69c0e268f25d0
```

Conversion used Google's WOFF2 encoder 1.0.2 with default settings. To regenerate
the files, verify and extract the archive, then run in its extracted directory:

```sh
for font in LiberationSans-*.ttf; do
    woff2_compress "$font"
done
```

Copy the four `.woff2` files here and preserve the upstream `LICENSE` as
`OFL.txt`. When updating the fonts, also update their notice in
`ThirdPartyNotices.txt`. The converter is needed only when regenerating these
files; ordinary builds copy them directly.

`frontend/build.mjs` substitutes the four resource names in the bundled PDF.js
worker and excludes its original Liberation TTF files. This relies on the
reader using browser font substitution (`useSystemFonts: true`) with XFA
disabled. PDF.js's XFA font substitution uses TTF glyph tables and cannot use
these WOFF2 resources directly.
