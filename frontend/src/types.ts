export interface Author {
    name: string;
    sort_name: string;
    role?: string;
}

export interface AuthorAdmin {
    name: string;
    sort_name: string;
    book_count: number;
}

export interface SeriesSummary {
    name: string;
    author: string;
    book_count: number;
    finished_count: number;
    cover_work_id: string;
    cover_version: number;
}

export interface CursorPage<T> {
    items: T[];
    next_cursor?: string;
}

export interface BookSummary {
    id: string;
    title: string;
    authors_list: Author[];
    authors_display: string;
    series: string | null;
    series_index: number | null;
    tags: string | null;
    date: string | null;
    year?: string;
    has_cover: boolean;
    cover_version: number;
    assets: Asset[];
}

export interface Book extends BookSummary {
    sort_title?: string;
    description_source: string | null;
    description_html: string | null;
    language: string | null;
    language_name?: string;
    publisher: string | null;
    identifiers: string | null;
    date_human?: string;
    added_at?: number;
    updated_at?: number;
    writeback?: BookWriteback;
    reading_status: ReadingStatusState;
}

export type ReadingStatus = 'unread' | 'reading' | 'finished' | 'dropped';

export interface ReadingStatusState {
    status: ReadingStatus;
    updated_at?: number;
}

// BookWriteback drives the admin-only "Write metadata to file" action:
// available only in manual mode with a writable asset; dirty when the file is
// behind the catalog (enabled vs "up to date").
export interface BookWriteback {
    available: boolean;
    dirty: boolean;
}

export interface BookWritebackResult {
    written: number;
    unchanged: number;
    failed: number;
    errors?: string[];
    book: Book;
}

export interface BookSequenceItem {
    id: string;
    title: string;
}

export interface BookSequenceWindow {
    items: BookSequenceItem[];
    current_index: number;
    total?: number;
}

export interface BookJump {
    label: string;
    offset: number;
}

export interface BookJumps {
    items: BookJump[];
    total: number;
}

export interface TrashedBook extends BookSummary {
    deleted_at: number;
    deleted_by?: string;
}

export interface BookImportResult {
    status: 'imported' | 'duplicate' | 'restored';
    book: Book;
    asset_id?: string;
    warnings?: string[];
}

export interface MetadataCandidate {
    provider: string;
    provider_name: string;
    provider_id: string;
    cover_url?: string;
    title?: string;
    authors?: string;
    series?: string;
    series_index?: number;
    description?: string;
    tags?: string;
    language?: string;
    publisher?: string;
    date?: string;
    identifiers?: string;
}

export interface CoverSearchResult {
    token: string;
    preview_url: string;
    source: string;
    width: number;
    height: number;
}

export interface DownloadAsOption {
    target: string;
    label: string;
}

export interface Asset {
    id: string;
    extension: string;
    size?: number;
    is_primary: boolean;
    can_read: boolean;
    download_as?: DownloadAsOption[];
}

export interface ReaderLocator {
    engine?: string;
    cfi?: string;
    fraction?: number;
    page?: number;
    zoom?: number;
    [key: string]: unknown;
}

export interface ReaderState {
    asset_id: string;
    work_id: string;
    progress: number;
    locator: ReaderLocator;
    last_read_at?: number;
    updated_at?: number;
    reading_status: ReadingStatusState;
    status_changed?: boolean;
    status_transition_id?: string;
}

export type AnnotationKind = 'highlight';
export type AnnotationColor = 'yellow';

export interface Annotation {
    id: string;
    asset_id: string;
    kind: AnnotationKind;
    cfi: string;
    quote: string;
    context_before: string;
    context_after: string;
    note?: string;
    color: AnnotationColor;
    created_at: number;
    updated_at: number;
}

export interface ContinueReadingItem extends BookSummary {
    asset_id: string;
    progress: number;
    last_read_at: number;
}

export type ReaderFlow = 'paginated' | 'scrolled';
export type ReaderDisplayStyle = 'original' | 'paper' | 'custom';

export interface ReaderPreferences {
    epub_flow: ReaderFlow;
    display_style: ReaderDisplayStyle;
    font_scale: number;
    custom_column_width: number;
    custom_line_height: number;
    updated_at?: number;
}

export type ThemePreference = 'system' | 'light' | 'dark' | 'sepia';

export interface UserSettings {
    theme: ThemePreference;
    hide_continue_reading: boolean;
    updated_at?: number;
}

export type AccountRole = 'admin' | 'member' | 'reader';
export type ContentScope = 'all' | 'shelves';

export interface CurrentUser {
    id: string;
    username: string;
    role: AccountRole;
    content_scope: ContentScope;
}

export interface UserAccount {
    id: string;
    username: string;
    role: AccountRole;
    content_scope: ContentScope;
    scope_shelf_ids?: string[];
    // Shared shelves this user owns; deleting the user removes them for everyone,
    // so the delete dialog warns with them.
    shared_shelf_names?: string[];
    created_at?: number;
    updated_at?: number;
}

export interface AppToken {
    id: string;
    name: string;
    created_at: number;
    last_used_at?: number;
}

export interface CreatedAppToken {
    name: string;
    token: string;
}

export interface KoboConnection {
    id: string;
    shelf_id: string;
    shelf_name: string;
    created_at: number;
    updated_at: number;
    last_used_at?: number;
}

export interface CreatedKoboConnection extends KoboConnection {
    setup_url: string;
}

export interface IngestStatus {
    enabled: boolean;
    delete_sources: boolean;
    path: string;
    reachable: boolean;
    running: boolean;
    pending: number;
    last_scan_at?: number;
    last_import_at?: number;
    last_error?: string;
}

export interface AdminStorageStatus {
    books: BooksStorage;
    ingest: IngestStatus;
    layout: FileLayoutStorage;
    writeback: WritebackStatus;
}

export interface FileLayoutStorage {
    template: string;
}

export interface WritebackStatus {
    mode: 'off' | 'manual' | 'auto';
    pending: number;
    failed: number;
}

export interface BooksStorage {
    path: string;
    reachable: boolean;
    fs_type: string;
    network: boolean;
    book_count: number;
    size_bytes: number;
    free_bytes: number; // -1 when the filesystem can't report free space
}

export interface StorageScanResult {
    imported: number;
    duplicates: number;
    trashed: number;
    restored: number;
    failed: number;
    storage: AdminStorageStatus;
}

export interface FolderImportPreview {
    path: string;
    files: number;
    calibre_books: number;
    would_import: number;
    duplicates: number;
    trashed: number;
    skipped: number;
    failed: number;
    errors?: string[];
}

export interface FolderImportResult {
    path: string;
    files: number;
    calibre_books: number;
    imported: number;
    duplicates: number;
    trashed: number;
    restored: number;
    skipped: number;
    failed: number;
    warnings: number;
    errors?: string[];
    storage: AdminStorageStatus;
}

export type DeliveryPreset = 'kindle' | 'pocketbook' | 'generic';

export interface EmailDeliverySettings {
    configured: boolean;
    host: string;
    port: number;
    security: 'auto' | 'starttls' | 'ssl' | 'plain';
    username: string;
    password_set: boolean;
    from_address: string;
    from_name: string;
    attachment_limit_mb: number;
}

export interface DeliveryDevice {
    id: string;
    name: string;
    email: string;
    preset: DeliveryPreset;
    is_default: boolean;
    created_at?: number;
    updated_at?: number;
}

export interface DeliveryPlan {
    asset_id?: string;
    format?: string;
    target?: string;
    filename?: string;
    media_type?: string;
    size_estimate?: number;
    converted?: boolean;
}

export interface DeliveryReason {
    code: string;
    message: string;
}

export interface SendOption {
    device: DeliveryDevice;
    plan?: DeliveryPlan;
    choices?: DeliveryPlan[];
    reason?: DeliveryReason;
}

export interface SendOptions {
    configured: boolean;
    devices: SendOption[];
    reason?: string;
}

export type DeliveryJobStatus = 'queued' | 'converting' | 'sending' | 'sent' | 'failed';

export interface DeliveryJob {
    id: string;
    device_id?: string;
    device_name: string;
    device_email: string;
    preset: DeliveryPreset;
    work_id: string;
    asset_id?: string;
    title: string;
    target?: string;
    filename: string;
    size_bytes?: number;
    status: DeliveryJobStatus;
    error?: string;
    created_at: number;
    updated_at: number;
    sent_at?: number;
}

export type ShelfKind = 'manual' | 'query';
export type ShelfVisibility = 'personal' | 'shared';

export interface Shelf {
    id: string;
    name: string;
    kind: ShelfKind;
    query?: string;
    owner_id: string;
    visibility: ShelfVisibility;
    position: number;
}

export interface SearchQueryValidation {
    valid: boolean;
    error?: string;
}

export interface BookShelfMembership extends Shelf {
    in_shelf: boolean;
}

export interface BookUpdate {
    title: string;
    sort_title?: string | null;
    authors: string | null;
    series: string | null;
    series_index: number | null;
    description: string | null;
    tags: string | null;
    language: string | null;
    publisher: string | null;
    date: string | null;
    identifiers: string | null;
}

export type BookPatch = Partial<BookUpdate>;

export type BulkTagMode = 'add' | 'remove' | 'replace' | 'clear';
export type BulkSeriesIndexMode = 'keep' | 'clear' | 'assign';

export interface BulkTagOperation {
    type: 'tags';
    mode: BulkTagMode;
    values?: string[];
}

export interface BulkSeriesSetOperation {
    type: 'series';
    mode: 'set';
    name: string;
    index?: { mode: BulkSeriesIndexMode; start?: number; step?: number };
}

export interface BulkSeriesClearOperation {
    type: 'series';
    mode: 'clear';
}

export interface BulkAuthorsSetOperation {
    type: 'authors';
    mode: 'set';
    authors: string;
}

export type BulkOperation =
    | BulkTagOperation
    | BulkSeriesSetOperation
    | BulkSeriesClearOperation
    | BulkAuthorsSetOperation;

export interface BulkEditRequest {
    ids: string[];
    operations: BulkOperation[];
}

export interface BulkEditResult {
    selected: number;
    changed: number;
    unchanged: number;
    relayout_warnings: number;
    books: BookSummary[];
}

export interface BulkTrashResult {
    trashed: number;
    ids: string[];
}

export interface BulkWritebackResult {
    selected: number;
    queued: number;
}

export interface WritebackRetryResult {
    queued: number;
    storage: AdminStorageStatus;
}

export type BulkShelfOp = 'add' | 'remove';

export interface BulkShelfResult {
    changed: number;
}

export interface CleanupCategory {
    count: number;
}

export interface DuplicateGroup {
    reason: string;
    key: string;
    books: BookSummary[];
}

export interface PossibleDuplicatesCategory {
    count: number;
    groups: DuplicateGroup[];
}

export interface Cleanup {
    missing_cover: CleanupCategory;
    unknown_author: CleanupCategory;
    no_tags: CleanupCategory;
    no_description: CleanupCategory;
    possible_duplicates: PossibleDuplicatesCategory;
}

export interface CleanupDuplicateMergeResult {
    survivor: BookSummary;
    trashed_ids: string[];
    relayout_warnings: number;
}
