# polka

polka is a self-hosted, managed ebook library for one person or a household.

Add books, find them quickly, fix metadata when it matters, and read, open, or
download them from a browser. Like calibre, polka owns the library for you:
books come in through import or upload, metadata lives in SQLite, and files are
kept in a clean, predictable layout on disk, so you can still read, copy, and
back them up directly.

It is opinionated but tries to stay out of the way: sensible defaults, few
settings, and no sense that running a library has become a second job. It is not
trying to replace calibre's desktop workbench — converters, plugins, custom
columns — or grow into a large media platform. It is a quieter,
lower-maintenance home for your books.

## What you can do

- **Add books** from the command line, by uploading or dragging them into the
  browser, from a folder already mounted on the server, or by dropping files
  into an incoming folder.
- **Find them** with fast search, shelves, saved searches, series pages, and
  tags. Search stays fast at tens of thousands of books; large catalogs also
  get a compact alphabetic jump rail when browsing by title or author.
- **Fix metadata and covers** when you care to. Books are usable immediately,
  and the details can improve later — one at a time, in bulk, or with
  suggestions fetched from Open Library and Google Books (only when you ask).
- **Read in the browser** with your place remembered. Reflowable books also
  have in-book search and personal highlights with notes, exportable per book
  as a standalone HTML file. OPDS and KOReader progress sync let external
  readers in too; Kobo sync can project one chosen shelf into the device's
  native library.
- **Send books to a device** by email — Kindle and PocketBook presets, or any
  address — converting on the way when the device needs a different format.

## Formats

polka imports EPUB, FB2 (including `.fb2.zip`), PDF, Kindle formats (MOBI, AZW,
AZW3, AZW4), comic archives (CBZ, CBR, CB7), DjVu, TXT, Markdown, HTML, DOCX,
ODT, RTF, and CHM. Conversion is built in, with no external tools.

## For a household

polka works for one person with no sharing workflow in the way, and scales to a
few people without much ceremony. Everyone shares the same catalog, while
reading position, personal shelves, and preferences stay personal. You can also
give someone an account that only sees the shelves you choose.

## Quick start

```bash
polka import ~/books --data ./library
polka serve --data ./library --addr 127.0.0.1:8080
```

`import` creates the library on first run. You can also start with `serve` and
add books from the browser. Open `http://127.0.0.1:8080`; the first run shows a
one-time setup page for the first account, which becomes the administrator.
From there, add books and invite the rest of the household.

The user guide lives in [`docs/guide.md`](docs/guide.md).

## Status

polka works and is used daily, but is still at an early stage. Expect changes,
and keep independent backups of your books and library data.

## Development

For development, `make serve` builds and starts a local library with account
auth enabled; `make help` lists the rest.
