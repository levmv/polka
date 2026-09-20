export type WasmSource = string | URL | BufferSource | WebAssembly.Module;
export interface Point { x: number; y: number }
export interface Rect extends Point { width: number; height: number }
export type Matrix = [number, number, number, number, number, number];
/** Counterclockwise quarter turns: 1 = 90°, 2 = 180°, 3 = 270°. */
export type QuarterTurns = 0 | 1 | 2 | 3;
export interface RenderOptions {
  /** Integer reduction factor, 1..256. Default 1; 2 halves each dimension. */
  subsample?: number;
  /** Fit inside this pixel box, preserving aspect ratio (rounded down, minimum 1 pixel).
   * Can enlarge. Uses area filtering when shrinking and bilinear filtering when enlarging.
   * IW44 reconstruction uses 1/2/4 reduction selected from the whole fitted page;
   * tiles agree, and mask coverage and palette assignments are preserved.
   * Full-resolution views and subsample use full reconstruction.
   * Excludes subsample other than 1; region refers to this fitted page.
   */
  size?: { width: number; height: number } | null;
  /** Additional rotation on top of the page's stored orientation. Default 0. */
  rotation?: QuarterTurns;
  /** Output pixels after reduction and rotation, top-left origin. Must fit wholly inside the page. */
  region?: Rect | null;
}
export interface Geometry extends Rect {
  pageWidth: number; pageHeight: number;
  /** Combined stored and requested rotation. */
  rotation: QuarterTurns;
  /** Unrotated top-left INFO coordinates to local output coordinates, and back. */
  matrix: Matrix; inverse: Matrix;
}
/** Owned, tightly packed RGBA pixels, top-to-bottom. stride = width * 4. */
export interface Raster {
  width: number; height: number; stride: number; rgba: ArrayBuffer;
}
export interface RenderedPage extends Raster {
  x: number; y: number; pageWidth: number; pageHeight: number;
}
export interface Diagnostics {
  /** Current and peak budgeted allocations; excludes JS buffers, Canvas and WASM capacity. */
  liveBytes: number; peakBytes: number;
  /** Reclaimable components/dictionaries; excludes the original input and active layers. */
  cacheBytes: number;
  /** Allocated WASM address space, including free allocator blocks; does not shrink. */
  linearBytes: number;
  /** Dictionary decodes since open. */
  dictionaryDecodes: number;
  /** Most recently started render/thumbnail, including cancelled work. Reset by open/close. */
  steps: number;
  /** Longest individual WASM step; does not bound cancellation latency. */
  maxStepMs: number;
}
export type DjvuErrorCode = 'InvalidData' | 'Unsupported' | 'LimitExceeded' | 'Cancelled'
  | 'OutOfMemory' | 'InvalidArgument' | 'Busy' | 'MissingComponent' | 'ComponentLoadFailed'
  | 'SourceReadFailed' | 'WasmLoadFailed' | 'WorkerFailed' | 'Destroyed';
/** Operational failures use this class. Branch on code; message is for display/debugging.
 * Core memory budget errors include the operation and refused allocation, before cleanup. */
export class DjvuError extends Error {
  readonly code: DjvuErrorCode;
  readonly cause?: unknown;
  constructor(code: DjvuErrorCode, message?: string, options?: { cause?: unknown });
}
export interface Component {
  index: number; id: string; name: string; title: string;
  kind: 'shared' | 'page' | 'thumbnail' | 'shared-annotations'; size: number; loaded: boolean;
  /** Byte range in a bundled source, or null for complete buffers and external files. */
  range: { offset: number; length: number } | null;
}
/** Snapshot at open, not updated when components are loaded later. */
export interface PageInfo {
  id: string; name: string; title: string; loaded: boolean;
  width?: number; height?: number; rotation?: QuarterTurns; error?: DjvuErrorCode;
}
export interface DocumentInfo { pages: PageInfo[]; indirect: boolean }
export type LoadComponent = (component: Component, options: { signal: AbortSignal }) => ArrayBuffer | RandomAccessSource | Promise<ArrayBuffer | RandomAccessSource>;
export interface OpenOptions {
  memoryLimit?: number;
  /** Idle cache target in bytes (default: memoryLimit / 4). Active work may exceed it. */
  cacheLimit?: number;
  loadComponent?: LoadComponent;
}
/** Immutable input, at most 0xffffffff bytes. The reader owns IO and optional caching. */
export interface RandomAccessSource {
  size: number;
  /** Return exactly length bytes. Each returned ArrayBuffer is transferred to the Worker.
   * Requests may overlap or run concurrently; respect signal to stop unnecessary IO.
   */
  read(offset: number, length: number, options: { signal: AbortSignal }): ArrayBuffer | Promise<ArrayBuffer>;
}
/** Unrotated top-left INFO bounds; start/length index PageText.bytes, not the JS string. */
export interface TextZone extends Rect {
  type: 'page' | 'column' | 'region' | 'paragraph' | 'line' | 'word' | 'character';
  parent: number | null; start: number; length: number; subtreeEnd: number;
}
/** Original bytes and a display string with UTF-8 replacement where needed. */
export interface PageText {
  text: string; bytes: ArrayBuffer; hasReplacements: boolean; zones: TextZone[];
}
export interface OutlineEntry { title: string; href: string; parent: number | null; subtreeEnd: number }
export interface Outline { entries: OutlineEntry[] }
export interface Link { kind: 'none' | 'page' | 'url' | 'options' | 'unresolved'; page: number | null }
export interface Border {
  kind: 'none' | 'xor' | 'solid' | 'shadow_in' | 'shadow_out' | 'shadow_ein' | 'shadow_eout';
  color: number | null; width: number | null;
}
export interface AreaStyle {
  border: Border | null; alwaysVisible: boolean; highlight: number | null;
  /** DjVu's 0..200 scale, or null when absent. */
  opacity: number | null;
  arrow: boolean; lineWidth: number | null; lineColor: number | null; background: number | null;
  textColor: number | null; pushpin: boolean;
}
/** Bounds and points use the same unrotated top-left INFO coordinates as text zones. */
export interface AnnotationArea {
  expression: number; href: string; target: string | null; comment: string;
  shape: 'rect' | 'oval' | 'text' | 'poly' | 'line'; bounds: Rect; points: Point[]; style: AreaStyle;
}
export interface PrintStrings { left: string | null; center: string | null; right: string | null }
export interface Annotations {
  source: string; bytes: ArrayBuffer; hasReplacements: boolean; legacyEscapes: boolean;
  chunks: { component: number; start: number; length: number }[];
  expressions: { start: number; length: number }[];
  view: { background: number | null; zoom: string | null; mode: string | null;
    alignment: { horizontal: string; vertical: string } | null };
  areas: AnnotationArea[]; metadata: { key: string; value: string; expression: number }[];
  xmp: string | null; header: PrintStrings; footer: PrintStrings;
}

/** Owned fields and XMP packets. page is zero-based, or null for shared annotations. */
export interface DocumentMetadata {
  metadata: { key: string; value: string; page: number | null }[];
  xmp: { value: string; page: number | null }[];
}

/** One Worker, one document and one render at a time. Page indexes are zero-based. */
export class DjvuDecoder {
  private constructor();
  /** Omit source when djvutang.wasm and worker.mjs sit beside decoder.mjs.
   * Explicit workerUrl supports separate asset paths in bundled applications.
   */
  static create(source?: WasmSource, options?: { workerUrl?: string | URL }): Promise<DjvuDecoder>;
  /** Replaces the document and cancels pending operations. ArrayBuffer input is transferred,
   * detaching it in the caller. A range source stays with its host until close.
   * Metadata results and rasters are owned snapshots. Await open before page operations.
   */
  open(input: ArrayBuffer | RandomAccessSource, options?: OpenOptions): Promise<DocumentInfo>;
  /** Reads page geometry without decoding layers. Matrices map unrotated top-left INFO pixels
   * to the output region and back. Use this for dimensions after loading indirect components.
   */
  geometry(page: number, options?: RenderOptions): Promise<Geometry>;
  /** Cancels a previous render/thumbnail with Cancelled. Metadata reads can run concurrently. */
  render(page: number, options?: RenderOptions): Promise<RenderedPage>;
  /** Stored thumbnail at its encoded size/orientation, or null. Shares render's slot and
   * cancellation. May load THUM or the page component; never renders a fallback preview.
   */
  storedThumbnail(page: number): Promise<Raster | null>;
  text(page: number): Promise<PageText | null>;
  annotations(page: number): Promise<Annotations | null>;
  /** Complete scan without image decoding. Empty arrays confirm absence; failures reject.
   * Preserves key case, unknown keys, duplicate entries, XMP packets and page scope.
   */
  metadata(): Promise<DocumentMetadata>;
  /** Stops the document metadata scan between bounded steps and source reads. */
  cancelMetadata(): Promise<void>;
  outline(): Promise<Outline | null>;
  resolveLink(href: string, fromPage?: number | null): Promise<Link>;
  /** Cancels render/thumbnail with Cancelled; metadata reads continue. */
  cancelRender(): Promise<void>;
  /** Cancels all pending operations and drops cached components and decoded layers. */
  dropCache(): Promise<void>;
  diagnostics(): Promise<Diagnostics>;
  /** Cancels pending operations and releases the document. Keeps the Worker for another open. */
  close(): Promise<void>;
  /** Cancels pending operations and terminates the Worker. Safe to repeat; later calls reject with Destroyed. */
  destroy(): void;
}
/** Decode the zone's byte range without confusing UTF-8 offsets with JS string indexes. */
export function zoneText(layer: PageText, zone: Pick<TextZone, 'start' | 'length'>): string;
export function mapPoint(matrix: Matrix, point: Point): Point;
export function mapRect(matrix: Matrix, rect: Rect): Rect;
