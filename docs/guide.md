# polka guide

polka is still pre-release: screens and commands may change, and existing
databases do not yet have a compatibility guarantee.

## First run

polka is a single binary. It does not need a separate database server or
conversion tools.

### Build from source

Building requires Go 1.27 or newer, Node.js 24 or newer with npm, and
Make. These are build-time requirements only.

The first build downloads dependencies and prepares the bundled PDF renderer;
later builds reuse it.

```bash
make build
./polka import ~/books --data ./library
./polka serve --data ./library --addr 127.0.0.1:8080
```

Open `http://127.0.0.1:8080`. The first visit shows a setup page; the account
created there becomes the administrator. Importing first is optional: `serve`
can create an empty library, and you can add books from the browser.

Library commands use `--data <dir>` or the `POLKA_DATA` environment variable to
find the library. The flag can appear before or after the command.
Command flags can also follow filenames: `polka meta book.epub --json`.
For filenames starting with `-`, put `--` before them: `polka meta -- -draft.epub`.

If the book files should live on another disk, set their location before the
first import:

```bash
polka storage root set /mnt/big-disk/books --data ./library
polka import ~/books --data ./library
```

## Data and book files

polka keeps application data and book files separate, although both live under
the data directory by default:

- **The data directory** is the path passed with `--data`. This is polka's own
  home: the catalog database, covers, settings, caches, temp space, and the
  incoming folder. Everything polka knows *about* your books lives here. It
  stays small compared to the books and is happiest on a fast local disk.
- **The books folder** contains ordinary book files in predictable author and
  title folders. Its default location is `<data>/books`; an administrator can
  place it on another disk with `polka storage root set`. Settings → Storage
  shows the active location and its health.

You can read, copy, and back up the books folder with normal tools. Do not
rename or move files inside it: polka tracks their exact paths and does not
periodically rescan the folder. To change the layout, use a storage path
template and let polka move the files.

The `<data>/cache` directory is disposable and can be omitted from backups.
Everything else in the data directory is part of the library. Run only one
polka server against a library.

## Add books

A book becomes searchable and downloadable as soon as it is imported; fixing
its metadata is optional and can happen later.

- **Browser:** use *Add book* in the sidebar, or drag files onto the library.
- **Incoming folder:** place files in `<data>/ingest`. The running server picks
  them up automatically; `polka ingest` processes the folder once from the
  command line.
- **Command line:** `polka import <file-or-folder>` imports files recursively.
- **Mounted server folder:** in Settings → Storage → *Add existing books*, an
  administrator can preview and import a folder already visible to the server.
- **calibre library:** point `polka import` at the calibre library. A folder
  with `metadata.opf`, a cover, and several formats becomes one book with
  multiple files attached.

For books with several files, polka prefers EPUB, then other reflowable ebook
formats, over fixed-page copies. Adding or merging in an EPUB can therefore
make it the primary file in place of an existing PDF or MOBI.

If a KFX book comes as several files, put the complete book folder in a ZIP
archive and change its extension to `.kfx-zip` before importing. Importing the
parts separately does not assemble a book.

Imports copy books into managed storage and leave their sources unchanged by
default. Importing the same file again is safe: matching content is recognized
instead of added twice.

An imported book's *Added* date may come from calibre's timestamp or the earliest
source file modification time, so it can be older than the import itself.

To supply a separate cover during import, place an image named `cover` next to
the book. It takes precedence over the embedded cover.

Source deletion must be enabled explicitly for the incoming folder or with
`--delete-sources`. A source is removed only after it is imported or recognized
as a duplicate. A calibre book folder is handled as one unit: polka removes the
whole folder, including sidecars and files it did not import, only after all of
its book files have been handled.

## Find and organize books

Search accepts free text and qualifiers:

```text
dune                      words in titles, authors, series, genres, or tags
author:herbert            author names only
series:"Foundation"       quotes keep a phrase together
genre:sci-fi title:dune   qualifiers can be combined
tag:"favourite"           books with this tag
genre:"Fiction"           Fiction and all its subgenres
genre:="Fiction"          books assigned directly to Fiction
status:unread             your unread books
status:dropped dune       status and text can be combined
no:cover                  books without a selected cover
no:author                 books with no author recorded
no:genres                 books with no genres recorded
```

Search completes the final word as you type: `fo` finds Foundation and
`author:herb` finds Herbert. Quote a term or phrase to keep it exact.
Use dots to nest genres and tags, for example `Fiction.Science Fiction`.
A quoted name selects that branch, ignoring letter case.
Add `=` before the quoted name to select only books assigned
directly to that node. Leave the value unquoted to search its words as you type.

Reading status is personal, so `status:` can produce different results for
different accounts even when the rest of the query is shared. `/` focuses the
search field, and `Esc` clears it.

Shelves can be filled manually or backed by a saved search. A saved-search
shelf updates whenever books begin or stop matching its query. To save the
current search as a shelf, use the bookmark button in the search field.
Shelves can be personal or shared.

## Edit and clean up the catalog

The book editor accepts multiple authors and partial publication dates such
as `1965` or `1965-08`. `Ctrl/Cmd-S` saves your changes.
Previous and Next follow the library, shelf, or search result you opened the
book from, which is useful for editing a group in sequence.

Cover and metadata lookup run only when requested. Metadata candidates from
Open Library and Google Books can be applied field by field; polka otherwise
works offline.

Members and administrators can select several books in the library and edit
authors, genres, tags, series numbering, and shelf membership in bulk.
The Authors page renames an author across the catalog, merges duplicate spellings, and overrides
automatic sort names. The Genres page contains separate genre and tag lists;
renaming a path updates its whole branch on every book that uses it. Existing
paths merge within that list. Saved searches using quoted paths follow the rename.

Metadata used by the storage path template also controls the corresponding
folders on disk. Changing a title or author therefore moves the managed files;
an administrator can opt into other fields such as series through a custom
template.

Metadata edits are always saved in polka. In Settings → General,
administrators choose whether supported book files are updated manually (the
default), automatically, or not at all. Manual mode adds single-book and bulk
*Write metadata* actions. EPUB, KEPUB, and FB2 are currently writable; a failed
file update leaves the saved catalog metadata intact and can be retried.

In EPUB and KEPUB, genres are written as subjects and tags as the Calibre
custom column `#extra_tags`. An imported `#genre` column is replaced by those
subjects when writing metadata. Apps without custom-column support may show
only the genres.

Removing a book sends it to Trash without deleting its files. Members can
restore it; permanent deletion and emptying Trash are administrator-only. The
Library action menu also opens Cleanup, which collects metadata-gap searches
and likely duplicate books.

## Highlights and notes

Export highlights and notes from the book page. Choose HTML for reading or
printing, Markdown for editing, or Web Annotation (JSON-LD) for tools that
support that standard.

## Other devices and reading apps

External apps use **app passwords**, not the account password. Create one per
device in Settings → Reading apps. An app password can read the catalog and
update that account's reading progress, but cannot sign into the web app or
edit and administer the shared library.

Use plain HTTP only on a trusted network, and use HTTPS or a VPN elsewhere.

### OPDS

Connect an OPDS client such as KOReader, Moon+ Reader, or PocketBook to
`http://your-host/opds` with username `polka` and an app password. The
catalog includes search, series, tags, and the shelves visible to that account.
Apps that support OPDS Progression can also sync reading positions with the
web reader.

### KOReader progress sync

Set **Custom sync server** to `http://your-host/kosync`. Choose **Login** with
username `polka` and an app password from Settings → Reading apps. Then
enable **Automatically keep documents in sync**.

Read EPUB books in KOReader or polka's web reader and continue where you left
off. The position may be approximate: you might resume at the start of a paragraph.

### Kobo native sync

In Settings → Reading apps → Kobo sync, choose one shelf visible to the account
and create a setup URL. On a mounted Kobo, open
`.kobo/Kobo/Kobo eReader.conf` and set `api_endpoint` to that URL under
`[OneStoreServices]`, then safely eject and sync.

polka sends EPUB and KEPUB books from the shelf, generating KEPUB from EPUB
when necessary, and removes books from the device after they leave the shelf.
Use Change shelf to choose another shelf without changing the setup URL. Books
outside the new shelf are removed on the next sync. Deleting the selected shelf
also removes its books from Kobo on the next sync; you can select a new shelf in
Settings. Revoking the connection invalidates its URL and stops syncing, leaving
books already downloaded to the device in place.

Reading position and read status sync with your polka account. Sync the Kobo
before switching readers, and again before continuing on the device. Passage
matching is approximate when moving between readers. Keep the device's clock
correct so an older offline update does not replace more recent reading.

Treat the setup URL as a password: use HTTPS or a trusted private network.

### Email delivery

To enable email delivery, an administrator turns on Sending in
Settings → Email delivery and configures SMTP. Each account can then add its
Kindle, PocketBook, or other email destinations in the same section and send
books from the book page.
When necessary, polka converts the book to a format accepted by the device.

### Download and conversion

The book page offers conversion options for each file's format. A particular
file may still be encrypted or contain unsupported content; polka checks it when
you request the conversion. The library copy stays unchanged. This includes the
*Repaired EPUB* option, which fixes recoverable EPUB packaging problems in the
downloaded copy.

## Accounts and access

The catalog is shared, while reading positions, statuses, highlights, notes,
reader settings, personal shelves, and app passwords belong to an account.

- **Reader** can browse, read, download, sync progress, and manage personal
  shelves, but cannot change the shared catalog.
- **Member** can also add books, edit metadata and covers, manage shared
  shelves, and move books to Trash.
- **Admin** can additionally manage accounts and storage and delete
  books permanently.

A Reader account can be restricted to selected shelves. Those shelves then
become the account's entire visible library, which is useful for children or
guests.

Changing your password signs out your other browsers. An admin reset, including
`polka user passwd <username>`, signs out all browsers for that account. Reading
apps keep working until you revoke their app password or Kobo connection.

## Maintenance and recovery

### Backups

Back up the data directory and the books folder. With the default layout this
means backing up one directory; if the books folder is elsewhere, both are
required. The cache can be skipped. Stop the server while copying so the
catalog database is captured at rest.

Restoring is the reverse: put both locations back and start polka with `--data`
pointing to the restored data directory.

### Check and repair

`polka check` compares the catalog with the files on disk. It is read-only and
safe to run at any time. The default pass is quick; `polka check --deep` reads
the files to verify integrity and built-in reader support.

`polka repair` attempts safe fixes after interrupted imports or metadata writes
and for recoverable path and cover problems or inconsistencies between the
catalog and its files. It leaves missing files and unexpected content changes
for manual action. Stop the server before running it. Repair reads and hashes
every book file, so it is a full-library recovery operation rather than routine
maintenance.

### NAS and unavailable storage

The books folder can live on a NAS. The data directory can use network storage
too, but a local disk is the better default: it stays small, and the catalog is
faster and avoids network-filesystem quirks. While the books folder is
unavailable, browsing, search, covers, and metadata editing continue to work;
opening, downloading, converting, or adding book files fails until storage
returns.

polka pauses writes if a books folder that should contain books suddenly
appears empty. This protects against the common case where a disconnected mount
reveals an empty underlying directory. It cannot distinguish the intended share
from a different non-empty directory mounted at the same path. If that matters,
configure the polka service to depend on the mount. Normal access resumes after
the share is remounted.

### Moving the library

To move only the books folder, stop the server, copy the folder while
preserving relative paths, then run:

```bash
polka storage root set /mnt/new/books --data ./library
```

The command verifies that every cataloged file exists under the new location
before saving it; it does not copy files itself.

To move the whole library to another machine, stop polka and copy the data
directory plus any external books folder. Keep the same external path, or set
the new books location before starting the server.

To change the folder naming scheme, use `polka storage template preview` and
inspect the proposed moves before `polka storage template apply`. Do not
reorganize managed files by hand.

## Command line

`polka help` lists all commands. File tools work without a library:

```bash
polka meta book.epub
polka meta book.djvu --cover cover.jpg
polka convert --to epub in.fb2 out.epub
```

`meta --cover` saves the book's cover image.

Library administration is also available for headless servers and scripts:

```bash
polka user ...
polka token ...
polka ingest
polka storage template ...
polka library shelves ...
polka library authors rename|merge
polka library writeback ...
```

Library commands take `--data <dir>` or the `POLKA_DATA` environment variable.
