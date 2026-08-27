# polka guide

polka is still pre-release: screens and commands may change, and existing
databases do not yet have a compatibility guarantee.

## First run

polka is a single binary. It does not need a separate database server or
conversion tools.

### Build from source

Building requires Go 1.27.0 or newer, Node.js 22.13 or newer with npm, and
Make. These are build-time requirements only.

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

Imports copy books into managed storage and leave their sources unchanged by
default. Metadata and covers are read from the files when available; books
without a usable cover get a generated one. Importing the same file again is
safe: matching content is recognized instead of added twice.

Source deletion must be enabled explicitly for the incoming folder or with
`--delete-sources`. A source is removed only after it is imported or recognized
as a duplicate. A calibre book folder is handled as one unit: polka removes the
whole folder, including sidecars and files it did not import, only after all of
its book files have been handled.

## Find and organize books

Search accepts free text and qualifiers:

```text
dune                      words in titles, authors, series, or tags
author:herbert            author names only
series:"Foundation"       quotes keep a phrase together
tag:sci-fi title:dune     qualifiers can be combined
status:unread             your unread books
status:dropped dune       status and text can be combined
no:cover                  books without a selected cover
```

Reading status is personal, so `status:` can produce different results for
different accounts even when the rest of the query is shared. `/` focuses the
search field, and `Esc` clears it.

Shelves can be filled manually or backed by a saved search. A saved-search
shelf updates whenever books begin or stop matching its query. After you enter
a search, a bookmark button appears inside the search field; use it to save the
current query as a shelf. Shelves can be personal or shared, while reading
progress always remains personal.

The Series page gives one entry per series. Opening one returns to the library
filtered to that series and sorted in volume order; series do not have separate
detail pages.

## Edit and clean up the catalog

The edit form keeps changes in a draft until *Save* or `Ctrl/Cmd-S`. It accepts
multiple authors and partial publication dates such as `1965` or `1965-08`.
Previous and Next follow the library, shelf, or search result you opened the
book from, which is useful for editing a group in sequence.

Cover and metadata lookup run only when requested. Metadata candidates from
Open Library and Google Books can be applied field by field; polka otherwise
works offline. A cover selected manually is not silently replaced by a later
import or maintenance pass.

Members and administrators can select several books in the library and edit
authors, tags, series numbering, and shelf membership in bulk. The Authors page
renames an author across the catalog, merges duplicate spellings, and overrides
automatic sort names.

Metadata used by the storage path template also controls the corresponding
folders on disk. Changing a title or author therefore moves the managed files;
an administrator can opt into other fields such as series through a custom
template.

Metadata edits are always saved in polka. In Settings → General,
administrators choose whether supported book files are updated manually (the
default), automatically, or not at all. Manual mode adds single-book and bulk
*Write metadata* actions. EPUB, KEPUB, and FB2 are currently writable; a failed
file update leaves the saved catalog metadata intact and can be retried.

Removing a book sends it to Trash without deleting its files. Members can
restore it; permanent deletion and emptying Trash are administrator-only. The
Library action menu also opens Cleanup, which collects metadata-gap searches
and likely duplicate books.

## Read and annotate

Reading position and display settings are saved per account. Books currently
being read appear in *Continue reading* on the library page.

Reading status is separate from saved position. Opening an unread book marks it
as *Reading*, and reaching the end marks it *Finished* with an immediate *Undo*
action. A status set manually to *Dropped* or *Finished* is not changed
automatically.

EPUB, FB2, and Kindle books support in-book search, paged or scrolled layout,
themes, typography controls, and text selection. Selected text can be saved as
a personal highlight with an optional note. Highlights are collected in the
reader and can be exported per book from **More actions** as HTML or Markdown.

## Other devices and reading apps

External apps use **app passwords**, not the account password. Create one per
device in Settings → Reading apps. An app password can read the catalog and
update that account's reading progress, but cannot sign into the web app or
edit and administer the shared library.

The password is shown once. Its setup screen also provides the OPDS URL and a
complete KOReader sync URL; copy anything you need before closing it. Use these
credentials over plain HTTP only on a trusted network, and use HTTPS or a VPN
elsewhere.

### OPDS

Connect an OPDS client such as KOReader, Moon+ Reader, or PocketBook to
`http://your-host/opds` with the account username and an app password. The
catalog includes search, series, tags, and the shelves visible to that account.

### KOReader progress sync

Set the copy-ready URL from the app-password setup screen as KOReader's custom
progress sync server. Positions then sync between KOReader devices. For books
downloaded from polka, progress also advances the corresponding library status
from unread to reading and then finished. Other books still sync between
KOReader devices without affecting the polka catalog.

### Kobo native sync

In Settings → Reading apps → Kobo sync, choose one shelf visible to the account
and create a setup URL. On a mounted Kobo, open
`.kobo/Kobo/Kobo eReader.conf` and set `api_endpoint` to that URL under
`[OneStoreServices]`, then safely eject and sync.

polka sends EPUB and KEPUB books from the shelf, generating KEPUB from EPUB
when necessary, and removes books from the device after they leave the shelf.
Replacing or revoking the connection invalidates the old URL. Kobo sync is
experimental and does not yet sync reading position. Treat its setup URL as a
password: use HTTPS or a trusted private network.

### Email delivery

An administrator configures SMTP once in Settings → Devices. Each account can
then add Kindle, PocketBook, or ordinary email destinations. When necessary,
polka converts the book to a format accepted by the selected device.

### Download and conversion

The book page offers conversions supported for each file. Conversion happens
when the file is requested and does not replace the library copy. This includes
the *Repaired EPUB* option, which fixes recoverable EPUB packaging problems in
the downloaded copy.

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
polka convert --to epub in.fb2 out.epub
```

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
