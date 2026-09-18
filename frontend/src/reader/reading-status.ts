import { undoReadingStatus } from '../api';
import { errorMessage } from '../errors';
import { showToast } from '../toast';
import type { ReaderPositionSaveResult } from '../types';

export function handleReadingStatusChange(state: ReaderPositionSaveResult): void {
    if (
        !state.status_changed ||
        state.reading_status?.status !== 'finished' ||
        !state.status_transition_id
    ) {
        return;
    }
    const eventID = state.status_transition_id;
    showToast('Marked as finished', {
        action: {
            label: 'Undo',
            onClick: () => {
                void undoReadingStatus(state.book_id, eventID)
                    .then((restored) => {
                        showToast(`Marked as ${restored.status}`);
                    })
                    .catch((error) => {
                        showToast(errorMessage(error), { type: 'error' });
                    });
            },
        },
    });
}
