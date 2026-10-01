import { Buffer } from 'node:buffer';
import { readFileSync } from 'node:fs';

export type UploadFile = {
  name: string;
  mimeType: string;
  buffer: Buffer;
};

const crcTable = new Uint32Array(256);
for (let n = 0; n < 256; n++) {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  crcTable[n] = c >>> 0;
}

function crc32(data: Buffer): number {
  let crc = 0xffffffff;
  for (const byte of data) crc = crcTable[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function xmlEscape(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function zipStore(files: { name: string; data: Buffer }[]): Buffer {
  const chunks: Buffer[] = [];
  const central: Buffer[] = [];
  let offset = 0;

  for (const file of files) {
    const name = Buffer.from(file.name);
    const crc = crc32(file.data);

    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0, 12);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(file.data.length, 18);
    local.writeUInt32LE(file.data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    chunks.push(local, name, file.data);

    const dir = Buffer.alloc(46);
    dir.writeUInt32LE(0x02014b50, 0);
    dir.writeUInt16LE(20, 4);
    dir.writeUInt16LE(20, 6);
    dir.writeUInt16LE(0, 8);
    dir.writeUInt16LE(0, 10);
    dir.writeUInt16LE(0, 12);
    dir.writeUInt16LE(0, 14);
    dir.writeUInt32LE(crc, 16);
    dir.writeUInt32LE(file.data.length, 20);
    dir.writeUInt32LE(file.data.length, 24);
    dir.writeUInt16LE(name.length, 28);
    dir.writeUInt16LE(0, 30);
    dir.writeUInt16LE(0, 32);
    dir.writeUInt16LE(0, 34);
    dir.writeUInt16LE(0, 36);
    dir.writeUInt32LE(0, 38);
    dir.writeUInt32LE(offset, 42);
    central.push(dir, name);

    offset += local.length + name.length + file.data.length;
  }

  const centralOffset = offset;
  const centralSize = central.reduce((sum, chunk) => sum + chunk.length, 0);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(centralSize, 12);
  end.writeUInt32LE(centralOffset, 16);
  end.writeUInt16LE(0, 20);
  return Buffer.concat([...chunks, ...central, end]);
}

function buildEPUB(
  title: string,
  author: string,
  name: string,
  options: {
    chapterName?: string;
    description?: string;
    verticalWriting?: boolean;
    scriptURL?: string;
    signature?: boolean;
  } = {},
): UploadFile {
  const chapterName = options.chapterName || 'chapter.xhtml';
  const description = options.description || '';
  const t = xmlEscape(title);
  const a = xmlEscape(author);
  const desc = description
    ? `\n    <dc:description>${xmlEscape(description)}</dc:description>`
    : '';
  const opf = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="bookid" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:uuid:${name}</dc:identifier>
    <dc:title>${t}</dc:title>
    <dc:creator>${a}</dc:creator>
    <dc:language>${options.verticalWriting ? 'ja' : 'en'}</dc:language>${desc}
  </metadata>
  <manifest><item id="chapter" href="${xmlEscape(chapterName)}" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`;
  const verticalStyle = options.verticalWriting
    ? '<style>body { writing-mode: vertical-rl; }</style>'
    : '';
  const scripts = options.scriptURL
    ? `<script>parent.document.documentElement.dataset.bookScript = 'inline';</script><script src="${xmlEscape(options.scriptURL)}"></script>`
    : '';
  const body = options.verticalWriting
    ? `<h1>${t}</h1><p>縦書きの合成テスト本文です。ページの高さと余白を確認します。</p><p>${a}</p>`
    : `<p>${a}</p>`;
  const chapter = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>${t}</title>${verticalStyle}${scripts}</head><body>${body}</body></html>`;
  const buffer = zipStore([
    { name: 'mimetype', data: Buffer.from('application/epub+zip') },
    {
      name: 'META-INF/container.xml',
      data: Buffer.from(
        `<?xml version="1.0" encoding="UTF-8"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
      ),
    },
    { name: 'OEBPS/content.opf', data: Buffer.from(opf) },
    { name: `OEBPS/${chapterName}`, data: Buffer.from(chapter) },
    ...(options.signature
      ? [{ name: 'META-INF/signatures.xml', data: Buffer.from('<signatures/>') }]
      : []),
  ]);
  return { name: `${name}.epub`, mimeType: 'application/epub+zip', buffer };
}

export function epubWithScripts(title: string, scriptURL: string, name: string): UploadFile {
  return buildEPUB(title, 'Script probe author', name, { scriptURL });
}

export function epub(title: string, author: string, name: string, description = ''): UploadFile {
  return buildEPUB(title, author, name, { description });
}

export function epubWithVerticalWriting(title: string, author: string, name: string): UploadFile {
  return buildEPUB(title, author, name, { verticalWriting: true });
}

export function paginationEPUB({
  vertical = false,
  rtl = false,
  importedStylesheet = 'imported.css',
  chapterCount = 3,
}: {
  vertical?: boolean;
  rtl?: boolean;
  importedStylesheet?: string;
  chapterCount?: number;
} = {}): UploadFile {
  const names = Array.from(
    { length: chapterCount },
    (_, index) => ['first', 'second', 'third'][index] ?? `chapter-${index + 1}`,
  );
  const chapters = names.map((name, index) => {
    const paragraphs = Array.from(
      { length: [8, 15, 5][index % 3] },
      (_, n) =>
        `<p id="p${n}">${name} paragraph ${n + 1}. ${'A small synthetic book checks real screen turns, chapter boundaries, and changing text size. '.repeat(3)}</p>`,
    ).join('');
    return {
      name: `OPS/${name}.xhtml`,
      data: Buffer.from(
        `<!DOCTYPE html><html xmlns="http://www.w3.org/1999/xhtml"><head><title>${name}</title><link rel="stylesheet" href="shared.css"/>${index === 2 ? `<style>@import url("${xmlEscape(importedStylesheet)}");</style><style/><style type="text/plain">Ignored text</style>` : ''}</head><body><h1>${name}</h1>${index > 0 ? '<img src="illustration.svg" alt="Synthetic illustration"/>' : ''}${paragraphs}${index === 2 ? '<div style="break-before:page"><img id="last-illustration" src="last.svg" loading="lazy" alt="Final illustration"/></div>' : ''}</body></html>`,
      ),
    };
  });
  const opf = `<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">pagination-${vertical}</dc:identifier><dc:title>Screen pagination</dc:title><dc:language>en</dc:language></metadata><manifest>${names.map((name) => `<item id="${name}" href="${name}.xhtml" media-type="application/xhtml+xml"${name === 'second' ? ' media-overlay="overlay"' : ''}/>`).join('')}<item id="css" href="shared.css" media-type="text/css"/><item id="imported-css" href="imported.css" media-type="text/css"/><item id="nested-css" href="nested.css" media-type="text/css"/><item id="font" href="font.woff2" media-type="font/woff2"/><item id="image" href="illustration.svg" media-type="image/svg+xml"/><item id="last-image" href="last.svg" media-type="image/svg+xml"/><item id="overlay" href="overlay.smil" media-type="application/smil+xml"/></manifest><spine page-progression-direction="${rtl ? 'rtl' : 'ltr'}">${names.map((name) => `<itemref idref="${name}"/>`).join('')}</spine></package>`;
  return {
    name: 'pagination.epub',
    mimeType: 'application/epub+zip',
    buffer: zipStore([
      { name: 'mimetype', data: Buffer.from('application/epub+zip') },
      {
        name: 'META-INF/container.xml',
        data: Buffer.from(
          '<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>',
        ),
      },
      { name: 'OPS/book.opf', data: Buffer.from(opf) },
      { name: 'OPS/imported.css', data: Buffer.from('@import "nested.css";') },
      { name: 'OPS/nested.css', data: Buffer.from('p#p0 { padding-top: 160px; }') },
      // Media-overlay metadata must not start a player in the measuring view.
      {
        name: 'OPS/overlay.smil',
        data: Buffer.from('<smil xmlns="http://www.w3.org/ns/SMIL" version="3.0"><body/></smil>'),
      },
      {
        name: 'OPS/shared.css',
        // Uppercase P must not match XHTML paragraphs, including when measured.
        data: Buffer.from(`@font-face { font-family: PaginationFixture; src: url(font.woff2); }
          P { padding: 80px !important; }
          body { ${vertical ? 'writing-mode: vertical-rl;' : ''} }
          p { font-family: PaginationFixture; }`),
      },
      {
        name: 'OPS/font.woff2',
        data: readFileSync(
          new URL('../../frontend/fonts/LiberationSans-Regular.woff2', import.meta.url),
        ),
      },
      {
        name: 'OPS/illustration.svg',
        data: Buffer.from(
          '<svg xmlns="http://www.w3.org/2000/svg" width="160" height="90"><rect width="160" height="90" fill="#789"/></svg>',
        ),
      },
      {
        name: 'OPS/last.svg',
        data: Buffer.from(
          '<svg xmlns="http://www.w3.org/2000/svg" width="400" height="550"><rect width="400" height="550" fill="#679"/></svg>',
        ),
      },
      ...chapters,
    ]),
  };
}

export function epubWithNonstandardZIPSignature(
  title: string,
  author: string,
  name: string,
): UploadFile {
  const fixture = buildEPUB(title, author, name, { signature: true });
  // A real-world EPUB producer emitted `Pk` instead of `PK` in the first
  // local ZIP signature. Go's bounded package reader can still resolve the
  // central directory, while browser ZIP sniffing rejects the source. The
  // KEPUB normalization pass repacks it with canonical signatures.
  fixture.buffer[1] = 0x6b;
  return fixture;
}

export function epubWithUnmarkedUTF8Entry(title: string, author: string, name: string): UploadFile {
  // The filename bytes are valid UTF-8, but the tiny ZIP writer deliberately
  // leaves the language-encoding flag clear, matching a real producer defect.
  return buildEPUB(title, author, name, { chapterName: '章.xhtml' });
}

export function fb2(title: string, author: string, name: string, body: string): UploadFile {
  const parts = author.trim().split(/\s+/);
  const last = parts.pop() || author;
  const first = parts.join(' ') || author;
  const buffer = Buffer.from(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description><title-info><author><first-name>${xmlEscape(first)}</first-name><last-name>${xmlEscape(last)}</last-name></author><book-title>${xmlEscape(title)}</book-title><lang>en</lang></title-info></description>
  <body><section><p>${xmlEscape(body)}</p></section></body>
</FictionBook>`);
  return { name: `${name}.fb2`, mimeType: 'application/xml', buffer };
}

function pdfEscape(value: string): string {
  return value.replace(/([\\()])/g, '\\$1');
}

export function pdf(
  title: string,
  author: string,
  name: string,
  rotations: number[] = [],
  secondLine = '',
): UploadFile {
  const pageLabels = ['First PDF page', 'Second PDF page', 'Third PDF page'];
  const fontStyles = ['Bold', 'Oblique', 'BoldOblique'];
  const fontResources = [
    '/F1 9 0 R',
    ...fontStyles.map((_, index) => `/F${index + 2} ${14 + index} 0 R`),
  ].join(' ');
  const objects = new Map<number, Buffer>();
  objects.set(1, Buffer.from('<< /Type /Catalog /Pages 2 0 R /Outlines 11 0 R >>'));
  objects.set(2, Buffer.from('<< /Type /Pages /Count 3 /Kids [3 0 R 5 0 R 7 0 R] >>'));

  for (let index = 0; index < pageLabels.length; index++) {
    const pageID = 3 + index * 2;
    const contentID = pageID + 1;
    const bottomTarget = index === 2 ? '\nBT /F1 24 Tf 72 72 Td (Bottom PDF target) Tj ET' : '';
    const extraLine = secondLine ? `\nBT /F1 24 Tf 72 660 Td (${pdfEscape(secondLine)}) Tj ET` : '';
    const fontSamples =
      index === 0
        ? fontStyles
            .map(
              (style, index) =>
                `\nBT /F${index + 2} 24 Tf 72 ${600 - index * 40} Td (Helvetica-${style}) Tj ET`,
            )
            .join('')
        : '';
    const content = Buffer.from(
      `BT /F1 24 Tf 72 700 Td (${pdfEscape(pageLabels[index])}) Tj ET${extraLine}${bottomTarget}${fontSamples}`,
    );
    objects.set(
      pageID,
      Buffer.from(
        `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Rotate ${rotations[index] ?? 0} /Resources << /Font << ${fontResources} >> >> /Contents ${contentID} 0 R >>`,
      ),
    );
    objects.set(
      contentID,
      Buffer.concat([
        Buffer.from(`<< /Length ${content.length} >>\nstream\n`),
        content,
        Buffer.from('\nendstream'),
      ]),
    );
  }

  objects.set(9, Buffer.from('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>'));
  objects.set(10, Buffer.from(`<< /Title (${pdfEscape(title)}) /Author (${pdfEscape(author)}) >>`));
  objects.set(11, Buffer.from('<< /Type /Outlines /First 12 0 R /Last 13 0 R /Count 2 >>'));
  objects.set(
    12,
    Buffer.from('<< /Title (Opening) /Parent 11 0 R /Next 13 0 R /Dest [3 0 R /Fit] >>'),
  );
  objects.set(
    13,
    Buffer.from('<< /Title (Final PDF page) /Parent 11 0 R /Prev 12 0 R /Dest [7 0 R /Fit] >>'),
  );

  for (const [index, style] of fontStyles.entries()) {
    objects.set(
      14 + index,
      Buffer.from(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-${style} >>`),
    );
  }

  const size = Math.max(...objects.keys()) + 1;
  const chunks: Buffer[] = [Buffer.from('%PDF-1.4\n')];
  const offsets = new Array<number>(size).fill(0);
  let offset = chunks[0].length;
  for (let id = 1; id < size; id++) {
    const object = Buffer.concat([
      Buffer.from(`${id} 0 obj\n`),
      objects.get(id) || Buffer.from('<<>>'),
      Buffer.from('\nendobj\n'),
    ]);
    offsets[id] = offset;
    chunks.push(object);
    offset += object.length;
  }

  const xrefOffset = offset;
  const xref = [
    'xref',
    `0 ${size}`,
    '0000000000 65535 f ',
    ...offsets.slice(1).map((value) => `${String(value).padStart(10, '0')} 00000 n `),
    'trailer',
    `<< /Size ${size} /Root 1 0 R /Info 10 0 R >>`,
    'startxref',
    String(xrefOffset),
    '%%EOF',
    '',
  ].join('\n');
  chunks.push(Buffer.from(xref));

  return {
    name: `${name}.pdf`,
    mimeType: 'application/pdf',
    buffer: Buffer.concat(chunks),
  };
}
