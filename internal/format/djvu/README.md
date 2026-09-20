# DjVuTang

`vendor/` contains the browser adapter and types from the pinned release.
The frontend build prepares `generated/djvutang.wasm` on first use and checks
its hash before reuse. Only the WASM binary is extracted from the release;
it stays outside Git. Typechecking needs no download or Go toolchain.

To update, replace the three files in `vendor/` and update `djvutang.json` from
the same release, checking the archive hash against its published checksum.
Then run `make test` and `make browser-test PWARGS='tests/djvu-reader.spec.ts'`.

DjVuTang's own MIT license and copyright match Polka's `LICENSE`, so only its
third-party notices need copying into `ThirdPartyNotices.txt`.
