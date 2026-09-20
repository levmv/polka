package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReadingStatusLifecycleHistoryAndIsolation(t *testing.T) {
	database := newTestDB(t)

	alice := mustUser(t, database, "alice", RoleMember)
	bob := mustUser(t, database, "bob", RoleMember)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_hash, current_hash)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', 'hash1', randomblob(16), randomblob(16));
	`)

	state, err := GetReadingStatus(database.Read(t.Context()), alice.ID, 1)
	if err != nil || state.Status != ReadingStatusUnread || state.UpdatedAt != 0 {
		t.Fatalf("default status = %+v, err %v", state, err)
	}

	if err := database.TouchReader(context.Background(), alice.ID, 1, ReadingStatusSourceWebReader); err != nil {
		t.Fatal(err)
	}
	opened, err := GetReadingStatus(database.Read(t.Context()), alice.ID, 1)
	if err != nil || opened.Status != ReadingStatusReading || opened.LastEventID == 0 {
		t.Fatalf("opened status = %+v, err %v", opened, err)
	}
	_, again, err := database.SaveReaderState(
		context.Background(), alice.ID, 1, testReaderWrite(0.5, Locator{}, 0), ReadingStatusSourceWebReader,
	)
	if err != nil || again.Changed || again.State.Status != ReadingStatusReading {
		t.Fatalf("repeated reading update = %+v, err %v", again, err)
	}

	_, finished, err := database.SaveReaderState(
		context.Background(), alice.ID, 1, testReaderWrite(ReaderFinishedProgress, Locator{}, 1), ReadingStatusSourceWebReader,
	)
	if err != nil || !finished.Changed || finished.State.Status != ReadingStatusFinished || finished.EventID == 0 {
		t.Fatalf("finished status = %+v, err %v", finished, err)
	}
	restored, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, 1, finished.EventID)
	if err != nil || restored.State.Status != ReadingStatusReading || restored.State.LastEventID != opened.LastEventID {
		t.Fatalf("undo finish = %+v, err %v", restored, err)
	}

	manualFinish, err := database.SetReadingStatus(context.Background(), alice.ID, 1, ReadingStatusFinished, ReadingStatusSourceManual)
	if err != nil {
		t.Fatalf("manual finish: %v", err)
	}
	if _, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, 1, manualFinish.EventID); !errors.Is(err, ErrReadingStatusUndoUnavailable) {
		t.Fatalf("undo manual finish err = %v; want unavailable", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), alice.ID, 1, ReadingStatusReading, ReadingStatusSourceManual); err != nil {
		t.Fatalf("read again: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), alice.ID, 1, ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish reread: %v", err)
	}

	rows, err := database.Read(t.Context()).Query(`
		SELECT to_status, reverted_at
		FROM user_book_reading_events
		WHERE user_id = ? AND book_id = ?
		ORDER BY id ASC
	`, alice.ID, 1)
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	defer rows.Close()
	eventCount := 0
	automaticFinishReverted := false
	finishedEvents := 0
	for rows.Next() {
		var toStatus string
		var revertedAt sql.NullInt64
		if err := rows.Scan(&toStatus, &revertedAt); err != nil {
			t.Fatalf("scan history: %v", err)
		}
		eventCount++
		if eventCount == 2 {
			automaticFinishReverted = toStatus == ReadingStatusFinished && revertedAt.Valid
		}
		if toStatus == ReadingStatusFinished && !revertedAt.Valid {
			finishedEvents++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read history: %v", err)
	}
	if eventCount != 5 || !automaticFinishReverted {
		t.Fatalf("history count = %d, automatic finish reverted = %t; want 5, true", eventCount, automaticFinishReverted)
	}
	if finishedEvents != 2 {
		t.Fatalf("durable finish events = %d; want 2 repeat-read completions", finishedEvents)
	}

	bobState, err := GetReadingStatus(database.Read(t.Context()), bob.ID, 1)
	if err != nil || bobState.Status != ReadingStatusUnread {
		t.Fatalf("bob status = %+v, err %v; alice state leaked", bobState, err)
	}
}

func TestAutomaticReadingStatusKeepsExplicitTerminalStates(t *testing.T) {
	database := newTestDB(t)
	user := mustUser(t, database, "reader", RoleReader)
	mustExec(t, database, `
		INSERT INTO books (id, title, sort_title) VALUES (1, 'Book', 'Book');
		INSERT INTO assets (id, book_id, storage_path, filename, extension, koreader_hash, original_hash, current_hash)
		VALUES (1, 1, 'book.epub', 'book.epub', '.epub', '55555555555555555555555555555555', randomblob(16), randomblob(16));
		INSERT INTO koreader_hashes(asset_id, hash) SELECT id, unhex(koreader_hash) FROM assets;
	`)

	dropped, err := database.SetReadingStatus(t.Context(), user.ID, 1, ReadingStatusDropped, ReadingStatusSourceManual)
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	for _, document := range []string{"55555555555555555555555555555555", "unknown"} {
		if _, err := database.SaveKOReaderProgress(t.Context(), user.ID, KOReaderProgress{
			DocumentHash: document, Position: "last-page", Progress: 1, DeviceName: "KOReader", DeviceID: "reader-a",
		}); err != nil {
			t.Fatalf("KOSync save: %v", err)
		}
		state, err := GetReadingStatus(database.Read(t.Context()), user.ID, 1)
		if err != nil || state != dropped.State {
			t.Fatalf("KOSync changed dropped status: %+v, err %v", state, err)
		}
	}
}
