package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// GetExternalKOReaderState reads the native fallback without resolving a library asset.
func GetExternalKOReaderState(queryer Queryer, userID int64, document string) (*KOReaderState, error) {
	state := &KOReaderState{}
	err := queryer.QueryRow(`SELECT position, progress, device_id, device_name, updated_at
        FROM koreader_external_positions WHERE user_id = ? AND document = ?`, userID, document).
		Scan(&state.Position, &state.Progress, &state.DeviceID, &state.DeviceName, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get external KOReader position: %w", err)
	}
	return state, nil
}

func saveExternalKOReaderState(tx *Tx, userID int64, document string, state *KOReaderState) error {
	_, err := tx.Exec(`INSERT INTO koreader_external_positions
        (user_id, document, position, progress, device_id, device_name, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(user_id, document) DO UPDATE SET
            position = excluded.position, progress = excluded.progress,
            device_id = excluded.device_id, device_name = excluded.device_name, updated_at = excluded.updated_at`,
		userID, document, state.Position, state.Progress, state.DeviceID, state.DeviceName, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save external KOReader position: %w", err)
	}
	return nil
}

func deleteExternalKOReaderState(tx *Tx, userID int64, document string) error {
	_, err := tx.Exec(`DELETE FROM koreader_external_positions WHERE user_id = ? AND document = ?`, userID, document)
	return err
}
