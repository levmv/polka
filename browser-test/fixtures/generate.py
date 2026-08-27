import sys
import os
import zipfile
import base64
from html import escape

# Minimal valid 1x1 PNG, embedded as a cover so importers/grids have a real
# image to load without a server-generated placeholder round trip.
PNG_1x1 = b'\x89PNG\x0d\x0a\x1a\x0a\x00\x00\x00\x0dIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\x0aIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\x0d\x0a\x2d\xb4\x00\x00\x00\x00IEND\xaeB`\x82'
AVIF_2x2 = base64.b64decode('AAAAIGZ0eXBhdmlmAAAAAGF2aWZtaWYxbWlhZk1BMUEAAADrbWV0YQAAAAAAAAAhaGRscgAAAAAAAAAAcGljdAAAAAAAAAAAAAAAAAAAAAAOcGl0bQAAAAAAAQAAAB5pbG9jAAAAAEQAAAEAAQAAAAEAAAETAAAATgAAAChpaW5mAAAAAAABAAAAGmluZmUCAAAAAAEAAGF2MDFDb2xvcgAAAABqaXBycAAAAEtpcGNvAAAAFGlzcGUAAAAAAAAAAgAAAAIAAAAQcGl4aQAAAAADCAgIAAAADGF2MUOBIAAAAAAAE2NvbHJuY2x4AAIAAgAAgAAAABdpcG1hAAAAAAAAAAEAAQQBAoMEAAAAVm1kYXQSAAoHOAA+UCAgCTJBEAAA/GkD1f4mq+MwFQdpuClS0/3si/n0TTr9ev16QLv////Vm4Y8wLMNz6Ex1IdSQsuhMdSHUh1IdSHUjV2E5TI=')

CONTAINER_XML = '''<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>'''


def _chapter_xhtml(title, author):
    safe_title = escape(title)
    safe_author = escape(author)
    paragraphs = '\n'.join(
        f'    <p>{escape("This is a browser-test reading paragraph")} {i}. '
        f'{safe_title} by {safe_author} keeps the EPUB reader fixture long enough '
        f'for pagination and progress smoke coverage.</p>'
        for i in range(1, 12)
    )
    return f'''<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
  <head><title>{safe_title}</title></head>
  <body>
    <h1>{safe_title}</h1>
{paragraphs}
  </body>
</html>'''


def _write_epub(filename, opf, include_cover, chapter):
    with zipfile.ZipFile(filename, 'w', zipfile.ZIP_STORED) as zf:
        zf.writestr('mimetype', 'application/epub+zip')
    with zipfile.ZipFile(filename, 'a', zipfile.ZIP_DEFLATED) as zf:
        zf.writestr('META-INF/container.xml', CONTAINER_XML)
        if include_cover:
            zf.writestr('OEBPS/cover.png', PNG_1x1)
        zf.writestr('OEBPS/content.opf', opf)
        zf.writestr('OEBPS/chapter.xhtml', chapter)


def create_epub(filename, title, author, include_cover):
    title_xml = escape(title)
    author_xml = escape(author)
    manifest = '<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>'
    if include_cover:
        manifest += '<item id="cover-image" href="cover.png" media-type="image/png"/>'

    opf = f'''<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:opf="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>{title_xml}</dc:title>
    <dc:creator>{author_xml}</dc:creator>
    <dc:language>eng</dc:language>
    <dc:publisher>Test Publisher</dc:publisher>
    <dc:date>2026-06-13T00:00:00+00:00</dc:date>
    <dc:description>&lt;p&gt;Some &lt;strong&gt;HTML&lt;/strong&gt; description. &lt;script&gt;alert(1);&lt;/script&gt;&lt;/p&gt;</dc:description>
    <dc:identifier id="isbn" opf:scheme="ISBN">1234567890</dc:identifier>
    <dc:identifier opf:scheme="GOOGLE">gBookId123</dc:identifier>
    <meta name="calibre:series" content="Test Series"/>
    {"<meta name='cover' content='cover-image'/>" if include_cover else ""}
  </metadata>
  <manifest>{manifest}</manifest>
  <spine><itemref idref="chapter"/></spine>
</package>'''
    _write_epub(filename, opf, include_cover, _chapter_xhtml(title, author))


def create_writeback_fixture(filename):
    # A neutral EPUB owned solely by the metadata write-back test. Unlike the
    # shared create_epub fixtures it carries no series/ISBN/publisher, so editing
    # and rewriting it cannot shift the series, duplicate, or identifier counts
    # other tests assert on.
    opf = '''<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Writeback Fixture</dc:title>
    <dc:creator>Writeback Author</dc:creator>
    <dc:language>eng</dc:language>
    <dc:date>2026-06-13</dc:date>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover-image" href="cover.png" media-type="image/png"/>
  </manifest>
  <spine><itemref idref="chapter"/></spine>
</package>'''
    _write_epub(filename, opf, include_cover=True,
                chapter=_chapter_xhtml('Writeback Fixture', 'Writeback Author'))


def create_fb2(filename, title, author):
    # Minimal FB2 the importer can read for metadata and foliate-js can render.
    # The body is padded so the reader has real content for progress/paging.
    safe_title = escape(title)
    safe_author = escape(author)
    first, _, last = author.partition(' ')
    paragraphs = '\n'.join(
        f'      <p>{escape("This is a browser-test FB2 paragraph")} {i}. '
        f'{safe_title} keeps the reader fixture long enough for smoke coverage.</p>'
        for i in range(1, 12)
    )
    fb2 = f'''<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
  <description>
    <title-info>
      <genre>sf</genre>
      <author><first-name>{escape(first)}</first-name><last-name>{escape(last)}</last-name></author>
      <book-title>{safe_title}</book-title>
      <annotation><p>An FB2 fixture for reader smoke coverage.</p></annotation>
      <lang>en</lang>
    </title-info>
    <document-info><id>fb2-reader-fixture</id></document-info>
  </description>
  <body>
    <section>
      <title><p>{safe_title}</p></title>
{paragraphs}
    </section>
  </body>
</FictionBook>'''
    with open(filename, 'w', encoding='utf-8') as f:
        f.write(fb2)


def create_cbz(filename, title, author):
    comic_info = f'''<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>{escape(title)}</Title>
  <Writer>{escape(author)}</Writer>
  <LanguageISO>en</LanguageISO>
</ComicInfo>'''
    with zipfile.ZipFile(filename, 'w', zipfile.ZIP_DEFLATED) as zf:
        zf.writestr('ComicInfo.xml', comic_info)
        for page in (1, 2, 10, 11):
            extension = 'bin' if page == 2 else 'avif' if page == 11 else 'png'
            zf.writestr(f'{page}.{extension}', AVIF_2x2 if page == 11 else PNG_1x1)


def create_filler_epub(filename, idx):
    # Lean book used only to push a library past the 50/page pagination
    # threshold. An old date keeps any real fixtures ahead of it under the
    # default (date-desc) sort; an embedded cover avoids a server-generated placeholder
    # round trip when the grid renders dozens of them.
    title = f'Filler Book {idx:03d}'
    manifest = (
        '<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/>'
        '<item id="cover-image" href="cover.png" media-type="image/png"/>'
    )
    opf = f'''<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" xmlns:opf="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>{title}</dc:title>
    <dc:creator>Filler Author</dc:creator>
    <dc:date>2000-01-01</dc:date>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>{manifest}</manifest>
  <spine><itemref idref="chapter"/></spine>
</package>'''
    _write_epub(filename, opf, include_cover=True, chapter=_chapter_xhtml(title, 'Filler Author'))


def main(argv):
    # `--filler N DIR` generates N throwaway books for pagination coverage
    # (not committed — the Makefile writes them into a gitignored temp dir).
    if argv[:1] == ['--filler']:
        count = int(argv[1])
        out_dir = argv[2]
        os.makedirs(out_dir, exist_ok=True)
        for i in range(1, count + 1):
            create_filler_epub(os.path.join(out_dir, f'filler-{i:03d}.epub'), i)
        return

    # Default: regenerate the committed smoke-test fixtures (run from this dir).
    create_epub('with-cover.epub', 'With Cover Book', 'Cover Author', True)
    create_epub('without-cover.epub', 'No Cover Book', 'Test Author', False)
    create_epub('duplicate-1.epub', 'Foundation', 'Isaac Asimov', False)
    create_epub('duplicate-2.epub', 'foundation!', 'isaac   asimov', False)
    # A book only the metadata write-back test touches, so it can edit and rewrite
    # the file without disturbing the shared fixtures other tests assert on.
    create_writeback_fixture('writeback.epub')
    create_fb2('reader.fb2', 'FB2 Reader Book', 'Fb Author')
    create_cbz('reader.cbz', 'CBZ Reader Book', 'Comic Author')


if __name__ == '__main__':
    main(sys.argv[1:])
