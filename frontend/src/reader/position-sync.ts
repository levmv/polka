import type { ReaderState, ReaderStateWrite } from '../types';

type Observation = Omit<ReaderStateWrite, 'revision'>;
export type PositionWriteResult =
    | { kind: 'saved'; state: ReaderState }
    | { kind: 'conflict' | 'rejected' | 'unconfirmed' };

interface PositionSyncIO {
    read(): Promise<ReaderState>;
    write(payload: ReaderStateWrite, keepalive: boolean): Promise<PositionWriteResult>;
    restore(state: ReaderState): Promise<void>;
    onSaved(state: ReaderState): void;
    retryLater(): void;
}

// The base revision tracks the local reading position. Defer remote positions
// until it is safe to navigate. Run reads and writes in sequence, keeping the
// latest pending position and the original body of an unconfirmed write.
export class ReaderPositionSync {
    private baseRevision: number | null = null;
    private pending: Observation | null = null;
    private unconfirmed: { observation: Observation; payload: ReaderStateWrite } | null = null;
    private deferred: ReaderState | null = null;
    private refreshRequested = false;
    private canRestore: (() => boolean) | null = null;
    private running: Promise<void> | null = null;
    private readonly io: PositionSyncIO;

    constructor(io: PositionSyncIO) {
        this.io = io;
    }

    initialize(state: ReaderState | null): void {
        this.baseRevision = state?.revision ?? null;
    }

    queue(observation: Observation): void {
        this.pending = observation;
    }

    refresh(canRestore?: () => boolean): Promise<void> {
        this.refreshRequested = true;
        if (canRestore) this.canRestore = canRestore;
        return this.flush();
    }

    flush(keepalive = false): Promise<void> {
        // A deferred winner may itself have changed since the last conflict.
        if (this.deferred && this.pending && !this.unconfirmed) this.refreshRequested = true;
        if (this.running) return this.running;
        this.running = this.run(keepalive).finally(() => {
            this.running = null;
            this.canRestore = null;
        });
        return this.running;
    }

    private async run(keepalive: boolean): Promise<void> {
        let conflicts = 0;
        while (this.refreshRequested || this.unconfirmed || this.pending || this.deferred) {
            // An uncertain write must be retried with exactly its original
            // body. Server equality/CAS checks distinguish acceptance from a
            // concurrent write before we merge the latest pending observation.
            if (this.refreshRequested && !this.unconfirmed && !(await this.readSaved())) return;
            if (this.deferred && !this.unconfirmed && !(await this.reconcileSaved())) return;
            if (!this.unconfirmed && !this.pending) {
                if (this.refreshRequested) continue;
                return;
            }
            if (this.baseRevision === null) {
                this.refreshRequested = true;
                continue;
            }
            const outcome = await this.writePending(keepalive);
            if (outcome === 'stop') return;
            if (outcome === 'conflict' && ++conflicts >= 3) {
                this.io.retryLater();
                return;
            }
        }
    }

    private async readSaved(): Promise<boolean> {
        this.refreshRequested = false;
        try {
            this.deferred = await this.io.read();
            return true;
        } catch {
            this.refreshRequested = true;
            this.io.retryLater();
            return false;
        }
    }

    private async reconcileSaved(): Promise<boolean> {
        const saved = this.deferred;
        if (!saved) return true;
        if (keepLocalReaderPosition(this.pending, this.baseRevision, saved)) {
            this.baseRevision = saved.revision;
            this.deferred = null;
        } else {
            this.pending = null;
            if (saved.revision === this.baseRevision) {
                this.deferred = null;
            } else if (this.canRestore?.()) {
                const previousRevision = this.baseRevision;
                this.baseRevision = saved.revision;
                this.deferred = null;
                try {
                    await this.io.restore(saved);
                } catch {
                    this.baseRevision = previousRevision;
                    this.deferred = saved;
                    return false;
                }
            }
        }
        return true;
    }

    private async writePending(keepalive: boolean): Promise<'continue' | 'conflict' | 'stop'> {
        if (!this.unconfirmed && this.pending && this.baseRevision !== null) {
            this.unconfirmed = {
                observation: this.pending,
                payload: { ...this.pending, revision: this.baseRevision },
            };
        }
        const request = this.unconfirmed;
        if (!request) return 'stop';
        const result = await this.io.write(request.payload, keepalive);
        switch (result.kind) {
            case 'saved':
                this.baseRevision = result.state.revision;
                if (this.pending === request.observation) this.pending = null;
                this.unconfirmed = null;
                this.deferred = null;
                if (!keepalive) this.io.onSaved(result.state);
                return 'continue';
            case 'conflict':
                this.unconfirmed = null;
                this.refreshRequested = true;
                return 'conflict';
            case 'rejected':
                if (this.pending === request.observation) this.pending = null;
                this.unconfirmed = null;
                return 'continue';
            case 'unconfirmed':
                this.io.retryLater();
                return 'stop';
        }
    }
}

// Only divergent new observations compete by progress. A saved local position
// is just an old copy and follows the server, including a move backwards.
// Reset deliberately follows this same rule: another open reader with pending
// progress may restore it after learning the reset's revision. We accept this
// rare race rather than persist separate reset history.
export function keepLocalReaderPosition(
    local: { progress: number } | null,
    revision: number | null,
    saved: Pick<ReaderState, 'revision' | 'progress'>,
): boolean {
    if (!local) return false;
    if (revision === saved.revision) return true;
    return local.progress > saved.progress;
}
