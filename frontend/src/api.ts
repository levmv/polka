import { type BookListContext, bookListContextParams } from './book-list-context';
import { takeBootstrapCurrentUser, takeBootstrapUserSettings } from './bootstrap';
import { browserTimeZone } from './time-zone';
import type {
    AdminStorageStatus,
    Annotation,
    AppToken,
    Author,
    AuthorAdmin,
    Book,
    BookImportResult,
    BookJumps,
    BookPatch,
    BookSequenceWindow,
    BookShelfMembership,
    BookSummary,
    BookWritebackResult,
    BulkEditRequest,
    BulkEditResult,
    BulkShelfOp,
    BulkShelfResult,
    BulkTrashResult,
    BulkWritebackResult,
    Cleanup,
    CleanupDuplicateMergeResult,
    ContinueReadingItem,
    CoverSearchResult,
    CreatedAppToken,
    CreatedKoboConnection,
    CurrentUser,
    CursorPage,
    DeliveryDevice,
    DeliveryJob,
    EmailDeliverySettings,
    FolderImportPreview,
    FolderImportResult,
    KoboConnection,
    MetadataCandidate,
    ReaderLocator,
    ReaderState,
    ReadingStatus,
    ReadingStatusState,
    SearchQueryValidation,
    SendOptions,
    SeriesSummary,
    Shelf,
    ShelfKind,
    StorageScanResult,
    TrashedBook,
    UserAccount,
    UserSettings,
    WritebackRetryResult,
    WritebackStatus,
} from './types';

let currentUserPromise: Promise<CurrentUser> | null = null;
let userSettingsPromise: Promise<UserSettings> | null = null;

// Catalog reads are local and normally complete in milliseconds. A deadline is
// still necessary because fetch has none of its own: a browser can otherwise
// leave a request pending forever after trying to reuse a stale connection.
// Safe reads get one fresh attempt after the first connection failure.
const READ_ATTEMPT_TIMEOUT_MS = 12_000;
const READ_ATTEMPTS = 2;

class APIConnectionError extends Error {
    constructor(
        public readonly cause: unknown,
        message = 'Cannot reach server',
    ) {
        super(message);
        this.name = 'APIConnectionError';
    }
}

function isAbortError(error: unknown): boolean {
    const name =
        error instanceof Error
            ? error.name
            : typeof error === 'object' && error !== null
              ? (error as { name?: string }).name
              : '';
    return name === 'AbortError';
}

function isNetworkError(error: unknown): boolean {
    if (error instanceof TypeError) return true;
    return error instanceof DOMException && error.name === 'NetworkError';
}

interface RequestDeadline {
    signal: AbortSignal;
    throwIfInterrupted(): void;
    waitFor<T>(operation: Promise<T>): Promise<T>;
    clear(): void;
}

function requestDeadline(callerSignal: AbortSignal | null | undefined): RequestDeadline {
    const controller = new AbortController();

    // WebKit can leave the fetch promise pending even after its signal is
    // aborted when a suspended page resumes onto a stale connection. Keep an
    // independent rejection path so application navigation and retries do not
    // depend on the networking process acknowledging abort.
    let rejectInterruption: (reason: unknown) => void = () => {};
    let interrupted = false;
    let interruptionReason: unknown;
    const interruption = new Promise<never>((_resolve, reject) => {
        rejectInterruption = reject;
    });
    const interrupt = (reason: unknown) => {
        if (interrupted) return;
        interrupted = true;
        interruptionReason = reason;
        rejectInterruption(reason);
    };

    const abortFromCaller = () => {
        interrupt(
            callerSignal?.reason ?? new DOMException('The operation was aborted', 'AbortError'),
        );
        controller.abort();
    };
    if (callerSignal?.aborted) {
        abortFromCaller();
    } else {
        callerSignal?.addEventListener('abort', abortFromCaller, { once: true });
    }

    const timer = globalThis.setTimeout(() => {
        interrupt(
            new APIConnectionError(
                new DOMException('The request timed out', 'TimeoutError'),
                'Server did not respond',
            ),
        );
        controller.abort();
    }, READ_ATTEMPT_TIMEOUT_MS);

    return {
        signal: controller.signal,
        throwIfInterrupted(): void {
            if (interrupted) throw interruptionReason;
        },
        waitFor: async <T>(operation: Promise<T>) => await Promise.race([operation, interruption]),
        clear(): void {
            globalThis.clearTimeout(timer);
            callerSignal?.removeEventListener('abort', abortFromCaller);
        },
    };
}

function requestMethod(input: RequestInfo | URL, init?: RequestInit): string {
    if (init?.method) return init.method.toUpperCase();
    if (typeof Request !== 'undefined' && input instanceof Request) {
        return input.method.toUpperCase();
    }
    return 'GET';
}

async function requestResult<T>(
    input: RequestInfo | URL,
    init: RequestInit | undefined,
    consume: (response: Response) => Promise<T>,
): Promise<T> {
    const safeRead = ['GET', 'HEAD'].includes(requestMethod(input, init));
    const attempts = safeRead ? READ_ATTEMPTS : 1;

    for (let attempt = 0; attempt < attempts; attempt++) {
        try {
            return await requestAttempt(input, init, consume, safeRead);
        } catch (error) {
            if (init?.signal?.aborted) throw error;
            if (!(error instanceof APIConnectionError) || attempt === attempts - 1) throw error;
        }
    }
    throw new Error('unreachable');
}

async function requestAttempt<T>(
    input: RequestInfo | URL,
    init: RequestInit | undefined,
    consume: (response: Response) => Promise<T>,
    withDeadline: boolean,
): Promise<T> {
    const deadline = withDeadline ? requestDeadline(init?.signal) : null;
    const requestInit = deadline ? { ...init, signal: deadline.signal } : init;
    const operation = (async () => {
        let response: Response;
        try {
            response = await fetch(input, requestInit);
        } catch (error) {
            deadline?.throwIfInterrupted();
            if (isAbortError(error)) throw error;
            throw new APIConnectionError(error);
        }
        // A broken networking process may deliver a late response after the
        // application has already timed out or left the route. Do not parse it
        // or perform response-side effects such as a 401 redirect.
        deadline?.throwIfInterrupted();

        try {
            return await consume(response);
        } catch (error) {
            deadline?.throwIfInterrupted();
            if (isNetworkError(error)) throw new APIConnectionError(error);
            throw error;
        }
    })();
    try {
        return await (deadline ? deadline.waitFor(operation) : operation);
    } finally {
        deadline?.clear();
    }
}

async function responseError(res: Response, fallback: string): Promise<Error> {
    if (res.status === 401) window.location.href = '/login';
    const text = await res.text();
    return new Error(text.trim() || fallback);
}

async function apiFetch(
    input: RequestInfo | URL,
    fallback: string | ((res: Response) => string),
    init?: RequestInit,
): Promise<Response> {
    return await requestResult(input, init, async (res) => {
        if (!res.ok) {
            throw await responseError(
                res,
                typeof fallback === 'function' ? fallback(res) : fallback,
            );
        }
        return res;
    });
}

async function fetchJSON<T>(
    input: RequestInfo | URL,
    fallback: string | ((res: Response) => string),
    init?: RequestInit,
): Promise<T> {
    return await requestResult(input, init, async (res) => {
        if (!res.ok) {
            throw await responseError(
                res,
                typeof fallback === 'function' ? fallback(res) : fallback,
            );
        }
        return await res.json();
    });
}

function jsonBody(method: string, payload: unknown): RequestInit {
    return {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
    };
}

export async function fetchCurrentUser(): Promise<CurrentUser> {
    if (!currentUserPromise) {
        const bootstrapped = takeBootstrapCurrentUser();
        const promise = bootstrapped?.id
            ? Promise.resolve(bootstrapped)
            : fetchJSON<CurrentUser>('/api/me', 'Failed to fetch current user');
        currentUserPromise = promise.finally(() => {
            currentUserPromise = null;
        });
    }
    return await currentUserPromise;
}

export async function fetchUserSettings(): Promise<UserSettings> {
    if (!userSettingsPromise) {
        const bootstrapped = takeBootstrapUserSettings();
        const promise = bootstrapped?.theme
            ? Promise.resolve(bootstrapped)
            : fetchJSON<UserSettings>('/api/settings', 'Failed to fetch settings');
        userSettingsPromise = promise
            .then(async (settings) => {
                if (settings.time_zone) return settings;
                try {
                    return await fetchJSON<UserSettings>(
                        '/api/settings',
                        'Failed to initialize time zone',
                        jsonBody('PUT', {
                            time_zone: browserTimeZone(),
                            initialize_time_zone: true,
                        }),
                    );
                } catch {
                    // Do not block browsing on a failed automatic preference save.
                    // A later fetch can retry the same conditional initialization.
                    return settings;
                }
            })
            .finally(() => {
                userSettingsPromise = null;
            });
    }
    return await userSettingsPromise;
}

export type UserSettingsUpdate = Partial<Omit<UserSettings, 'updated_at'>>;

export async function saveUserSettings(payload: UserSettingsUpdate): Promise<UserSettings> {
    const request: UserSettingsUpdate = {
        theme: payload.theme,
        show_continue_reading: payload.show_continue_reading,
        time_zone: payload.time_zone,
        reader_flow: payload.reader_flow,
        reader_style: payload.reader_style,
        reader_font_size: payload.reader_font_size,
        reader_column_width: payload.reader_column_width,
        reader_line_height: payload.reader_line_height,
    };
    const saved = await fetchJSON<UserSettings>(
        '/api/settings',
        'Failed to save settings',
        jsonBody('PUT', request),
    );
    userSettingsPromise = null;
    return saved;
}

export async function fetchContinueReading(limit: number = 8): Promise<ContinueReadingItem[]> {
    const params = new URLSearchParams({ limit: String(limit) });
    return await fetchJSON<ContinueReadingItem[]>(
        `/api/reader/continue?${params.toString()}`,
        'Failed to fetch continue reading',
    );
}

export async function fetchUsers(): Promise<UserAccount[]> {
    return await fetchJSON<UserAccount[]>('/api/users', 'Failed to fetch users');
}

export async function createUser(payload: {
    username: string;
    password: string;
    role: UserAccount['role'];
    content_scope?: UserAccount['content_scope'];
    scope_shelf_ids?: string[];
}): Promise<UserAccount> {
    return await fetchJSON<UserAccount>(
        '/api/users',
        'Failed to create user',
        jsonBody('POST', payload),
    );
}

export async function updateUserAccess(
    userId: number,
    payload: {
        role: UserAccount['role'];
        content_scope: UserAccount['content_scope'];
        scope_shelf_ids: string[];
    },
): Promise<UserAccount> {
    return await fetchJSON<UserAccount>(
        `/api/users/${encodeURIComponent(userId)}`,
        'Failed to update user access',
        jsonBody('PATCH', payload),
    );
}

export async function updateUserPassword(userId: number, password: string): Promise<void> {
    await apiFetch(
        `/api/users/${encodeURIComponent(userId)}/password`,
        'Failed to update password',
        jsonBody('POST', { password }),
    );
}

export async function deleteUser(userId: number): Promise<void> {
    await apiFetch(`/api/users/${encodeURIComponent(userId)}`, 'Failed to remove user', {
        method: 'DELETE',
    });
}

export async function fetchAppTokens(): Promise<AppToken[]> {
    return await fetchJSON<AppToken[]>('/api/app-tokens', 'Failed to fetch app passwords');
}

export async function fetchKoboConnection(): Promise<KoboConnection | null> {
    return await fetchJSON<KoboConnection | null>(
        '/api/kobo-connection',
        'Failed to fetch Kobo connection',
    );
}

export async function fetchAdminStorageStatus(): Promise<AdminStorageStatus> {
    return await fetchJSON<AdminStorageStatus>(
        '/api/admin/storage',
        'Failed to fetch storage status',
    );
}

export async function saveAdminStorageStatus(payload: {
    ingest?: {
        enabled?: boolean;
        delete_sources?: boolean;
        path?: string;
    };
    writeback?: {
        mode?: WritebackStatus['mode'];
    };
}): Promise<AdminStorageStatus> {
    return await fetchJSON<AdminStorageStatus>(
        '/api/admin/storage',
        'Failed to save storage settings',
        jsonBody('PATCH', payload),
    );
}

export async function scanIncomingFolder(): Promise<StorageScanResult> {
    return await fetchJSON<StorageScanResult>(
        '/api/admin/storage/scan',
        'Failed to scan incoming folder',
        { method: 'POST' },
    );
}

export async function previewFolderImport(path: string): Promise<FolderImportPreview> {
    return await fetchJSON<FolderImportPreview>(
        '/api/admin/storage/import/preview',
        'Failed to preview folder import',
        jsonBody('POST', { path }),
    );
}

export async function importServerFolder(path: string): Promise<FolderImportResult> {
    return await fetchJSON<FolderImportResult>(
        '/api/admin/storage/import',
        'Failed to import folder',
        jsonBody('POST', { path }),
    );
}

export async function retryFailedWriteback(): Promise<WritebackRetryResult> {
    return await fetchJSON<WritebackRetryResult>(
        '/api/admin/storage/writeback/retry',
        'Failed to retry metadata write-back',
        { method: 'POST' },
    );
}

export async function saveSendEnabled(enabled: boolean): Promise<boolean> {
    const saved = await fetchJSON<{ enabled: boolean }>(
        '/api/admin/delivery',
        'Failed to save sending setting',
        jsonBody('PUT', { enabled }),
    );
    return saved.enabled === true;
}

export async function fetchEmailDeliverySettings(): Promise<EmailDeliverySettings> {
    return await fetchJSON<EmailDeliverySettings>(
        '/api/admin/email',
        'Failed to fetch email settings',
    );
}

export type EmailDeliverySettingsUpdate = Partial<
    Pick<
        EmailDeliverySettings,
        | 'host'
        | 'port'
        | 'security'
        | 'username'
        | 'from_address'
        | 'from_name'
        | 'attachment_limit_mb'
    >
> & { password?: string };

export async function saveEmailDeliverySettings(
    payload: EmailDeliverySettingsUpdate,
): Promise<EmailDeliverySettings> {
    const request: EmailDeliverySettingsUpdate = {
        host: payload.host,
        port: payload.port,
        security: payload.security,
        username: payload.username,
        password: payload.password,
        from_address: payload.from_address,
        from_name: payload.from_name,
        attachment_limit_mb: payload.attachment_limit_mb,
    };
    return await fetchJSON<EmailDeliverySettings>(
        '/api/admin/email',
        'Failed to save email settings',
        jsonBody('PUT', request),
    );
}

export async function sendEmailDeliveryTest(to: string): Promise<void> {
    await apiFetch(
        '/api/admin/email/test',
        'Failed to send test message',
        jsonBody('POST', { to }),
    );
}

export async function fetchDeliveryDevices(): Promise<DeliveryDevice[]> {
    return await fetchJSON<DeliveryDevice[]>('/api/devices', 'Failed to fetch devices');
}

export async function createDeliveryDevice(payload: {
    name: string;
    email: string;
    preset?: DeliveryDevice['preset'];
    is_default?: boolean;
}): Promise<DeliveryDevice> {
    return await fetchJSON<DeliveryDevice>(
        '/api/devices',
        'Failed to create device',
        jsonBody('POST', payload),
    );
}

export async function updateDeliveryDevice(
    deviceId: string,
    payload: Partial<Pick<DeliveryDevice, 'name' | 'email' | 'preset' | 'is_default'>>,
): Promise<DeliveryDevice> {
    return await fetchJSON<DeliveryDevice>(
        `/api/devices/${encodeURIComponent(deviceId)}`,
        'Failed to update device',
        jsonBody('PATCH', payload),
    );
}

export async function deleteDeliveryDevice(deviceId: string): Promise<void> {
    await apiFetch(`/api/devices/${encodeURIComponent(deviceId)}`, 'Failed to delete device', {
        method: 'DELETE',
    });
}

export async function fetchSendOptions(bookId: string): Promise<SendOptions> {
    const params = new URLSearchParams({ book: bookId });
    return await fetchJSON<SendOptions>(
        `/api/send/options?${params.toString()}`,
        'Failed to fetch send options',
    );
}

export async function createDelivery(payload: {
    book_id: string;
    device_id?: string;
    asset_id?: string;
    target?: string;
}): Promise<DeliveryJob> {
    return await fetchJSON<DeliveryJob>(
        '/api/deliveries',
        'Failed to send book',
        jsonBody('POST', payload),
    );
}

export async function fetchDeliveryJob(jobId: string): Promise<DeliveryJob> {
    return await fetchJSON<DeliveryJob>(
        `/api/deliveries/${encodeURIComponent(jobId)}`,
        'Failed to fetch delivery status',
    );
}

export async function fetchDeliveries(limit: number = 20): Promise<DeliveryJob[]> {
    const params = new URLSearchParams({ limit: String(limit) });
    return await fetchJSON<DeliveryJob[]>(
        `/api/deliveries?${params.toString()}`,
        'Failed to fetch delivery history',
    );
}

export async function createAppToken(name: string): Promise<CreatedAppToken> {
    return await fetchJSON<CreatedAppToken>(
        '/api/app-tokens',
        'Failed to create app password',
        jsonBody('POST', { name }),
    );
}

export async function revokeAppToken(tokenId: string): Promise<void> {
    await apiFetch(
        `/api/app-tokens/${encodeURIComponent(tokenId)}`,
        'Failed to revoke app password',
        { method: 'DELETE' },
    );
}

export async function createKoboConnection(shelfId: string): Promise<CreatedKoboConnection> {
    return await fetchJSON<CreatedKoboConnection>(
        '/api/kobo-connection',
        'Failed to create Kobo connection',
        jsonBody('POST', { shelf_id: shelfId }),
    );
}

export async function revokeKoboConnection(): Promise<void> {
    await apiFetch('/api/kobo-connection', 'Failed to revoke Kobo connection', {
        method: 'DELETE',
    });
}

// The signal is caller-owned: a list request belongs to the route instance that
// started it, so a module-wide controller here would let one view cancel
// another's in-flight load.
export async function fetchBooks(
    query: string = '',
    sort: string = '',
    limit?: number,
    offset?: number,
    shelfId?: string,
    signal?: AbortSignal,
): Promise<BookSummary[]> {
    let url = '/api/books';
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    if (sort) params.set('sort', sort);
    if (limit != null) params.set('limit', String(limit));
    if (offset != null) params.set('offset', String(offset));
    if (shelfId) params.set('shelf', shelfId);

    if (params.toString()) {
        url += `?${params.toString()}`;
    }
    return await fetchJSON<BookSummary[]>(url, (res) => `HTTP error! status: ${res.status}`, {
        signal,
    });
}

export async function fetchBookJumps(sort: 'title' | 'author'): Promise<BookJumps> {
    const params = new URLSearchParams({ sort });
    return await fetchJSON<BookJumps>(
        `/api/books/jumps?${params.toString()}`,
        'Failed to fetch book jumps',
    );
}

export async function validateSearchQuery(query: string): Promise<SearchQueryValidation> {
    return await fetchJSON<SearchQueryValidation>(
        '/api/search/validate',
        'Failed to validate search query',
        jsonBody('POST', { query }),
    );
}

export async function fetchSeriesPage(
    cursor: string = '',
    query: string = '',
    limit?: number,
    signal?: AbortSignal,
): Promise<CursorPage<SeriesSummary>> {
    const params = new URLSearchParams();
    if (cursor) params.set('cursor', cursor);
    if (query) params.set('q', query);
    if (limit) params.set('limit', String(limit));
    const qs = params.toString();
    return await fetchJSON<CursorPage<SeriesSummary>>(
        `/api/series${qs ? `?${qs}` : ''}`,
        'Failed to fetch series',
        signal ? { signal } : undefined,
    );
}

export async function fetchShelves(): Promise<Shelf[]> {
    return await fetchJSON<Shelf[]>('/api/shelves', 'Failed to fetch shelves');
}

export async function createShelf(payload: {
    name: string;
    kind?: ShelfKind;
    query?: string;
    shared?: boolean;
}): Promise<Shelf> {
    return await fetchJSON<Shelf>(
        '/api/shelves',
        'Failed to create shelf',
        jsonBody('POST', payload),
    );
}

export async function updateShelf(
    shelfId: string,
    payload: {
        name?: string;
        query?: string;
        shared?: boolean;
    },
): Promise<Shelf> {
    return await fetchJSON<Shelf>(
        `/api/shelves/${encodeURIComponent(shelfId)}`,
        'Failed to update shelf',
        jsonBody('PATCH', payload),
    );
}

export async function deleteShelf(shelfId: string): Promise<void> {
    await apiFetch(`/api/shelves/${encodeURIComponent(shelfId)}`, 'Failed to delete shelf', {
        method: 'DELETE',
    });
}

export async function fetchBookShelves(bookId: string): Promise<BookShelfMembership[]> {
    return await fetchJSON<BookShelfMembership[]>(
        `/api/books/${encodeURIComponent(bookId)}/shelves`,
        'Failed to fetch book shelves',
    );
}

export async function addBookToShelf(shelfId: string, bookId: string): Promise<void> {
    await apiFetch(
        `/api/shelves/${encodeURIComponent(shelfId)}/books/${encodeURIComponent(bookId)}`,
        'Failed to add book to shelf',
        {
            method: 'PUT',
        },
    );
}

export async function removeBookFromShelf(shelfId: string, bookId: string): Promise<void> {
    await apiFetch(
        `/api/shelves/${encodeURIComponent(shelfId)}/books/${encodeURIComponent(bookId)}`,
        'Failed to remove book from shelf',
        {
            method: 'DELETE',
        },
    );
}

export async function fetchBook(bookId: string, signal?: AbortSignal): Promise<Book> {
    return await fetchJSON<Book>(`/api/books/${encodeURIComponent(bookId)}`, 'Not found', {
        signal,
    });
}

export async function writebackBook(bookId: string): Promise<BookWritebackResult> {
    return await fetchJSON<BookWritebackResult>(
        `/api/books/${encodeURIComponent(bookId)}/writeback`,
        'Failed to write metadata to file',
        { method: 'POST' },
    );
}

export async function fetchBookSequence(
    bookId: string,
    context: BookListContext,
    before = 25,
    after = 25,
): Promise<BookSequenceWindow> {
    const params = bookListContextParams(context);
    params.set('before', String(before));
    params.set('after', String(after));
    return await fetchJSON<BookSequenceWindow>(
        `/api/books/${encodeURIComponent(bookId)}/sequence?${params.toString()}`,
        'Failed to fetch book sequence',
    );
}

// Soft-delete (trash) a book. Reversible — the files stay until an admin purge.
export async function deleteBook(bookId: string): Promise<void> {
    await apiFetch(`/api/books/${encodeURIComponent(bookId)}`, 'Failed to remove book', {
        method: 'DELETE',
    });
}

export async function restoreBook(bookId: string): Promise<void> {
    await apiFetch(`/api/books/${encodeURIComponent(bookId)}/restore`, 'Failed to restore book', {
        method: 'POST',
    });
}

// Permanently delete a trashed book and its files. Admin-only (server-enforced).
export async function purgeBook(bookId: string): Promise<void> {
    await apiFetch(
        `/api/books/${encodeURIComponent(bookId)}/purge`,
        'Failed to permanently delete book',
        {
            method: 'DELETE',
        },
    );
}

export async function fetchTrash(signal?: AbortSignal): Promise<TrashedBook[]> {
    return await fetchJSON<TrashedBook[]>(
        '/api/trash',
        'Failed to load trash',
        signal ? { signal } : undefined,
    );
}

// Permanently delete every trashed book and its files. Admin-only
// (server-enforced); returns how many books were purged.
export async function emptyTrash(): Promise<{ purged: number }> {
    return await fetchJSON<{ purged: number }>('/api/trash', 'Failed to empty trash', {
        method: 'DELETE',
    });
}

export async function fetchReaderState(assetId: string): Promise<ReaderState> {
    return await fetchJSON<ReaderState>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/state`,
        'Failed to fetch reader state',
    );
}

export async function sendReadingActivity(
    assetId: string,
    sessionId: string,
    segment: number,
    checkpoint?: { elapsed_ms: number; last_activity_ms: number; finished: boolean },
    keepalive = false,
): Promise<{ active: boolean; counted_ms: number }> {
    return await requestAttempt(
        `/api/reader/assets/${encodeURIComponent(assetId)}/activity`,
        {
            ...jsonBody(checkpoint ? 'PUT' : 'POST', {
                session_id: sessionId,
                segment,
                ...checkpoint,
            }),
            keepalive,
        },
        async (response) => {
            if (!response.ok)
                throw await responseError(response, 'Failed to save reading activity');
            return await response.json();
        },
        true,
    );
}

export async function setReadingStatus(
    bookId: string,
    status: ReadingStatus,
): Promise<ReadingStatusState> {
    return await fetchJSON<ReadingStatusState>(
        `/api/books/${encodeURIComponent(bookId)}/reading-status`,
        'Failed to change reading status',
        jsonBody('PUT', { status }),
    );
}

export async function undoReadingStatus(
    bookId: string,
    eventId: string,
): Promise<ReadingStatusState> {
    return await fetchJSON<ReadingStatusState>(
        `/api/books/${encodeURIComponent(bookId)}/reading-status/undo`,
        'Failed to undo reading status',
        jsonBody('POST', { event_id: eventId }),
    );
}

export async function saveReaderState(
    assetId: string,
    payload: { progress: number; locator: ReaderLocator },
    options: { keepalive?: boolean } = {},
): Promise<ReaderState> {
    return await fetchJSON<ReaderState>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/state`,
        'Failed to save reader state',
        { ...jsonBody('PUT', payload), keepalive: options.keepalive },
    );
}

export async function resetReaderState(assetId: string): Promise<void> {
    await apiFetch(
        `/api/reader/assets/${encodeURIComponent(assetId)}/state`,
        'Failed to reset reader state',
        { method: 'DELETE' },
    );
}

export async function touchReaderState(assetId: string): Promise<ReaderState> {
    return await fetchJSON<ReaderState>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/touch`,
        'Failed to touch reader state',
        { method: 'POST' },
    );
}

export async function fetchAnnotations(assetId: string): Promise<Annotation[]> {
    return await fetchJSON<Annotation[]>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/annotations`,
        'Failed to fetch annotations',
    );
}

export async function createAnnotation(
    assetId: string,
    payload: {
        kind?: Annotation['kind'];
        cfi: string;
        quote: string;
        context_before?: string;
        context_after?: string;
        note?: string;
        color?: Annotation['color'];
    },
): Promise<Annotation> {
    return await fetchJSON<Annotation>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/annotations`,
        'Failed to create annotation',
        jsonBody('POST', payload),
    );
}

export async function updateAnnotationNote(
    assetId: string,
    annotationId: string,
    note: string,
): Promise<Annotation> {
    return await fetchJSON<Annotation>(
        `/api/reader/assets/${encodeURIComponent(assetId)}/annotations/${encodeURIComponent(annotationId)}`,
        'Failed to update annotation',
        jsonBody('PATCH', { note }),
    );
}

export async function deleteAnnotation(assetId: string, annotationId: string): Promise<void> {
    await apiFetch(
        `/api/reader/assets/${encodeURIComponent(assetId)}/annotations/${encodeURIComponent(annotationId)}`,
        'Failed to delete annotation',
        { method: 'DELETE' },
    );
}

export async function fetchMetadataCandidates(
    bookId: string,
    provider: string = 'openlibrary',
    signal?: AbortSignal,
): Promise<MetadataCandidate[]> {
    const params = new URLSearchParams();
    if (provider) params.set('provider', provider);
    return await fetchJSON<MetadataCandidate[]>(
        `/api/books/${encodeURIComponent(bookId)}/metadata-candidates?${params.toString()}`,
        'Failed to fetch metadata candidates',
        signal ? { signal } : undefined,
    );
}

// fetchMetadataDescription lazily loads one candidate's long description from a
// provider that supplies it out-of-band (Open Library search omits it). Returns
// '' when the provider has no extra description to offer.
export async function fetchMetadataDescription(
    provider: string,
    ref: string,
    signal?: AbortSignal,
): Promise<string> {
    const params = new URLSearchParams({ provider, ref });
    const data = await fetchJSON<{ description?: string }>(
        `/api/metadata/description?${params.toString()}`,
        'Failed to fetch description',
        signal ? { signal } : undefined,
    );
    return data.description || '';
}

export async function updateBook(bookId: string, payload: BookPatch): Promise<Book> {
    return await fetchJSON<Book>(
        `/api/books/${encodeURIComponent(bookId)}`,
        'Failed to update book',
        jsonBody('PATCH', payload),
    );
}

// bulkEditBooks applies operation-specific transforms (tags, series) to a set of
// loaded books. The server returns updated summaries so the caller can patch the
// rendered grid/table in place.
export async function bulkEditBooks(payload: BulkEditRequest): Promise<BulkEditResult> {
    return await fetchJSON<BulkEditResult>(
        '/api/books/bulk',
        'Bulk edit failed',
        jsonBody('PATCH', payload),
    );
}

// bulkTrashBooks soft-deletes a set of loaded books. The server returns the ids
// it actually trashed (visible, live), so the caller can drop just those rows.
export async function bulkTrashBooks(ids: string[]): Promise<BulkTrashResult> {
    return await fetchJSON<BulkTrashResult>(
        '/api/books/bulk/trash',
        'Failed to move books to Trash',
        jsonBody('POST', { ids }),
    );
}

export async function bulkWritebackBooks(ids: string[]): Promise<BulkWritebackResult> {
    return await fetchJSON<BulkWritebackResult>(
        '/api/books/bulk/writeback',
        'Failed to write metadata to files',
        jsonBody('POST', { ids }),
    );
}

// bulkShelfBooks adds or removes a set of books to/from one manual shelf. The
// server returns how many memberships actually changed (skipping already-present
// adds and absent removes).
export async function bulkShelfBooks(
    shelfId: string,
    ids: string[],
    op: BulkShelfOp,
): Promise<BulkShelfResult> {
    return await fetchJSON<BulkShelfResult>(
        `/api/shelves/${encodeURIComponent(shelfId)}/books/bulk`,
        'Shelf update failed',
        jsonBody('POST', { ids, op }),
    );
}

export async function uploadCover(bookId: string, file: File): Promise<Book> {
    const formData = new FormData();
    formData.append('cover', file);

    return await fetchJSON<Book>(
        `/api/books/${encodeURIComponent(bookId)}/cover`,
        'Request failed',
        {
            method: 'POST',
            body: formData,
        },
    );
}

export async function generateCoverPreview(
    bookId: string,
    payload: { title: string; author: string; seed?: number; style?: string },
): Promise<Blob> {
    const res = await apiFetch(
        `/api/books/${encodeURIComponent(bookId)}/cover-generated-preview`,
        'Request failed',
        jsonBody('POST', payload),
    );
    return await res.blob();
}

export async function searchCoverImages(
    bookId: string,
    title: string,
    author: string,
    signal?: AbortSignal,
): Promise<CoverSearchResult[]> {
    const params = new URLSearchParams({ title, author });
    return await fetchJSON<CoverSearchResult[]>(
        `/api/books/${encodeURIComponent(bookId)}/cover-search?${params.toString()}`,
        'Request failed',
        signal ? { signal } : undefined,
    );
}

export async function applyCoverSearchResult(bookId: string, token: string): Promise<Book> {
    return await fetchJSON<Book>(
        `/api/books/${encodeURIComponent(bookId)}/cover-search`,
        'Request failed',
        jsonBody('POST', { token }),
    );
}

export async function applyCoverURL(bookId: string, url: string): Promise<Book> {
    return await fetchJSON<Book>(
        `/api/books/${encodeURIComponent(bookId)}/cover-url`,
        'Request failed',
        jsonBody('POST', { url }),
    );
}

export async function uploadBook(file: File): Promise<BookImportResult> {
    const formData = new FormData();
    formData.append('book', file);

    return await fetchJSON<BookImportResult>('/api/import', 'Import failed', {
        method: 'POST',
        body: formData,
    });
}

export async function fetchAuthors(query: string = ''): Promise<Author[]> {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    return await fetchJSON<Author[]>(
        `/api/authors${params.toString() ? `?${params.toString()}` : ''}`,
        (res) => `HTTP error! status: ${res.status}`,
    );
}

export async function fetchTags(query: string = ''): Promise<string[]> {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    return await fetchJSON<string[]>(
        `/api/tags${params.toString() ? `?${params.toString()}` : ''}`,
        'Failed to fetch tags',
    );
}

export async function fetchCleanup(signal?: AbortSignal): Promise<Cleanup> {
    return await fetchJSON<Cleanup>(
        '/api/cleanup',
        'Failed to fetch cleanup items',
        signal ? { signal } : undefined,
    );
}

export async function mergeCleanupDuplicates(
    survivorId: string,
    bookIds: string[],
): Promise<CleanupDuplicateMergeResult> {
    return await fetchJSON<CleanupDuplicateMergeResult>(
        '/api/cleanup/duplicates/merge',
        'Failed to merge duplicates',
        jsonBody('POST', { survivor_id: survivorId, book_ids: bookIds }),
    );
}

export async function dismissCleanupDuplicates(bookIds: string[]): Promise<void> {
    await apiFetch(
        '/api/cleanup/duplicates/dismiss',
        'Failed to dismiss duplicates',
        jsonBody('POST', { book_ids: bookIds }),
    );
}

export async function fetchAuthorPage(
    cursor: string = '',
    signal?: AbortSignal,
): Promise<CursorPage<AuthorAdmin>> {
    const params = new URLSearchParams();
    if (cursor) params.set('cursor', cursor);
    const qs = params.toString();
    return await fetchJSON<CursorPage<AuthorAdmin>>(
        `/api/authors/list${qs ? `?${qs}` : ''}`,
        'Failed to fetch authors',
        signal ? { signal } : undefined,
    );
}

// fetchAuthorInfo returns one author's sort_name and book count by exact name,
// or null when no such author exists (404). Book edit uses the count for
// author-sort scope hints and conservative rename convergence prompts.
export async function fetchAuthorInfo(name: string): Promise<AuthorAdmin | null> {
    return await requestResult(
        `/api/authors/info?name=${encodeURIComponent(name)}`,
        undefined,
        async (res) => {
            if (res.status === 404) return null;
            if (!res.ok) throw await responseError(res, 'Failed to fetch author');
            return await res.json();
        },
    );
}

export interface AuthorOpResult {
    affected: number;
    moved: number;
    warnings: number;
}

export async function renameAuthor(oldName: string, newName: string): Promise<AuthorOpResult> {
    return await fetchJSON<AuthorOpResult>(
        '/api/authors/rename',
        'Rename failed',
        jsonBody('POST', { old: oldName, new: newName }),
    );
}

export async function setAuthorSortName(name: string, sortName: string): Promise<AuthorOpResult> {
    return await fetchJSON<AuthorOpResult>(
        '/api/authors/sort-name',
        'Update failed',
        jsonBody('POST', { name, sort_name: sortName }),
    );
}
