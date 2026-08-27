# Shared binary test fixtures

The embedded RAR3 and RAR5 CBR samples come from
[`ssokolow/rar-test-files`](https://github.com/ssokolow/rar-test-files). Each
contains only a synthetic 2×2 JPEG and PNG. Their author released the material
they own under CC0 specifically so the archives can be redistributed in test
suites.

Upstream SHA-256 checksums:

- `testfile.rar3.cbr`: `6598d1c5f7accfeefbda2bf03f934181486a42ea06a4d322d6016410d1a89cc1`
- `testfile.rar5.cbr`: `e8b106048f18e6fb9a5f8ec6a95346e76906e7e4e9ca15ec97e4f926159cb398`

The CB7 fixture is project-owned synthetic data: two copies of the existing
1×1 browser-test PNG plus a minimal `ComicInfo.xml`, packed as one solid LZMA2
archive with the official 7-Zip 26.02 console tool. The AVIF fixture was encoded
from four generated pixels by the pinned `gen2brain/avif` library. Neither
fixture contains third-party artwork.

The 442-byte lossless WebP is a synthetic 75×100 cover fixture shared by the
`covers` and `format` tests.
