# Shared binary test fixtures

The embedded RAR3 and RAR5 CBR samples come from
[`ssokolow/rar-test-files`](https://github.com/ssokolow/rar-test-files). Each
contains only a synthetic 2×2 JPEG and PNG; RAR5 uses solid compression. Their
author released the material they own under CC0 specifically so the archives
can be redistributed in test suites.

Upstream SHA-256 checksums:

- `testfile.rar3.cbr`: `6598d1c5f7accfeefbda2bf03f934181486a42ea06a4d322d6016410d1a89cc1`
- `testfile.rar5.solid.cbr`: `23ef370c58b7646d527106829410700ac314d86380b9c968a37066f39fe6c70b`

The CB7 fixture is project-owned synthetic data: two copies of the existing
1×1 browser-test PNG plus a minimal `ComicInfo.xml`, packed as one solid LZMA2
archive with the official 7-Zip 26.02 console tool. The AVIF fixture was encoded
from four generated pixels by the pinned `gen2brain/avif` library. Neither
fixture contains third-party artwork.

The 442-byte lossless WebP is a synthetic 75×100 cover fixture shared by the
`covers` and `format` tests.

`reader.djvu` contains three project-owned 600×800 pages: text, simple geometric
shapes, OCR text and an outline. The first and last pages use JB2; the middle
page uses IW44 color. Generated from a tiny Helvetica PDF using Poppler and
DjVuLibre (`cjb2`, `c44`, `djvm`, `djvused`); it contains no book content.
The server and browser tests share this fixture.

`blank-first.djvu` contains two white pages with a few scan specks followed by
a black rectangle, generated with `cjb2` and `djvm` for cover selection tests.

`metadata.djvu` is a 659-byte project-owned fixture with three identical 8×8
bitonal frames. Created with DjVuLibre (`cjb2`, `djvm`, `djvused`), it has shared
ANTz fields (`Creator: Scanner software`, `Language: eng`, `Year: 1843`) and
last-page ANTz containing `Title: Annotated DjVu` and XMP. XMP supplies the author
Ada Lovelace and an alternative title. It checks complete annotation discovery,
XMP fallback and metadata/cover independence without real book content.
