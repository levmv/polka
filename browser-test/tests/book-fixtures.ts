import { Buffer } from 'node:buffer';

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
  chapterName = 'chapter.xhtml',
  description = '',
): UploadFile {
  const t = xmlEscape(title);
  const a = xmlEscape(author);
  const desc = description ? `\n    <dc:description>${xmlEscape(description)}</dc:description>` : '';
  const opf = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="bookid" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">urn:uuid:${name}</dc:identifier>
    <dc:title>${t}</dc:title>
    <dc:creator>${a}</dc:creator>
    <dc:language>en</dc:language>${desc}
  </metadata>
  <manifest><item id="chapter" href="${xmlEscape(chapterName)}" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="chapter"/></spine>
</package>`;
  const chapter = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>${t}</title></head><body><p>${a}</p></body></html>`;
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
  ]);
  return { name: `${name}.epub`, mimeType: 'application/epub+zip', buffer };
}

export function epub(
  title: string,
  author: string,
  name: string,
  description = '',
): UploadFile {
  return buildEPUB(title, author, name, 'chapter.xhtml', description);
}

export function epubWithNonstandardZIPSignature(
  title: string,
  author: string,
  name: string,
): UploadFile {
  const fixture = buildEPUB(title, author, name);
  // A real-world EPUB producer emitted `Pk` instead of `PK` in the first
  // local ZIP signature. Go's bounded package reader can still resolve the
  // central directory, while browser ZIP sniffing rejects the source. The
  // KEPUB normalization pass repacks it with canonical signatures.
  fixture.buffer[1] = 0x6b;
  return fixture;
}

export function epubWithUnmarkedUTF8Entry(
  title: string,
  author: string,
  name: string,
): UploadFile {
  // The filename bytes are valid UTF-8, but the tiny ZIP writer deliberately
  // leaves the language-encoding flag clear, matching a real producer defect.
  return buildEPUB(title, author, name, '章.xhtml');
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

export function pdf(title: string, author: string, name: string): UploadFile {
  const pageLabels = ['First PDF page', 'Second PDF page', 'Third PDF page'];
  const objects = new Map<number, Buffer>();
  objects.set(1, Buffer.from('<< /Type /Catalog /Pages 2 0 R /Outlines 11 0 R >>'));
  objects.set(2, Buffer.from('<< /Type /Pages /Count 3 /Kids [3 0 R 5 0 R 7 0 R] >>'));

  for (let index = 0; index < pageLabels.length; index++) {
    const pageID = 3 + index * 2;
    const contentID = pageID + 1;
    const bottomTarget =
      index === 2 ? '\nBT /F1 24 Tf 72 72 Td (Bottom PDF target) Tj ET' : '';
    const content = Buffer.from(
      `BT /F1 24 Tf 72 700 Td (${pdfEscape(pageLabels[index])}) Tj ET${bottomTarget}`,
    );
    objects.set(
      pageID,
      Buffer.from(
        `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 9 0 R >> >> /Contents ${contentID} 0 R >>`,
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
  objects.set(
    10,
    Buffer.from(`<< /Title (${pdfEscape(title)}) /Author (${pdfEscape(author)}) >>`),
  );
  objects.set(11, Buffer.from('<< /Type /Outlines /First 12 0 R /Last 13 0 R /Count 2 >>'));
  objects.set(
    12,
    Buffer.from('<< /Title (Opening) /Parent 11 0 R /Next 13 0 R /Dest [3 0 R /Fit] >>'),
  );
  objects.set(
    13,
    Buffer.from(
      '<< /Title (Final PDF page) /Parent 11 0 R /Prev 12 0 R /Dest [7 0 R /Fit] >>',
    ),
  );

  const chunks: Buffer[] = [Buffer.from('%PDF-1.4\n')];
  const offsets = new Array<number>(14).fill(0);
  let offset = chunks[0].length;
  for (let id = 1; id <= 13; id++) {
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
    '0 14',
    '0000000000 65535 f ',
    ...offsets.slice(1).map((value) => `${String(value).padStart(10, '0')} 00000 n `),
    'trailer',
    '<< /Size 14 /Root 1 0 R /Info 10 0 R >>',
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
