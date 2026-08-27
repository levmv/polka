package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReadingStatusLifecycleHistoryAndIsolation(t *testing.T) {
	database := newTestDB(t)

	alice, err := database.CreateUser("alice", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := database.CreateUser("bob", "pw", RoleMember)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, koreader_hash)
		VALUES ('a1', 'w1', 'book.epub', 'book.epub', '.epub', 'hash1');
	`); err != nil {
		t.Fatalf("seed book: %v", err)
	}

	state, err := GetReadingStatus(database, alice.ID, "w1")
	if err != nil || state.Status != ReadingStatusUnread || state.UpdatedAt != 0 {
		t.Fatalf("default status = %+v, err %v", state, err)
	}

	_, opened, err := database.TouchReaderStateAndAdvanceStatus(context.Background(), alice.ID, "a1", ReadingStatusSourceWebReader)
	if err != nil || !opened.Changed || opened.State.Status != ReadingStatusReading {
		t.Fatalf("opened status = %+v, err %v", opened, err)
	}
	_, again, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), alice.ID, "a1", 0.5, EmptyReaderLocator(), ReadingStatusSourceWebReader,
	)
	if err != nil || again.Changed || again.State.Status != ReadingStatusReading {
		t.Fatalf("repeated reading update = %+v, err %v", again, err)
	}

	_, finished, err := database.SaveReaderStateAndAdvanceStatus(
		context.Background(), alice.ID, "a1", ReaderFinishedProgress, EmptyReaderLocator(), ReadingStatusSourceWebReader,
	)
	if err != nil || !finished.Changed || finished.State.Status != ReadingStatusFinished || finished.EventID == "" {
		t.Fatalf("finished status = %+v, err %v", finished, err)
	}
	restored, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, "w1", finished.EventID)
	if err != nil || restored.State.Status != ReadingStatusReading || restored.State.LastEventID != opened.EventID {
		t.Fatalf("undo finish = %+v, err %v", restored, err)
	}

	manualFinish, err := database.SetReadingStatus(context.Background(), alice.ID, "w1", ReadingStatusFinished, ReadingStatusSourceManual)
	if err != nil {
		t.Fatalf("manual finish: %v", err)
	}
	if _, err := database.UndoAutomaticReadingStatus(context.Background(), alice.ID, "w1", manualFinish.EventID); !errors.Is(err, ErrReadingStatusUndoUnavailable) {
		t.Fatalf("undo manual finish err = %v; want unavailable", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), alice.ID, "w1", ReadingStatusReading, ReadingStatusSourceManual); err != nil {
		t.Fatalf("read again: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), alice.ID, "w1", ReadingStatusFinished, ReadingStatusSourceManual); err != nil {
		t.Fatalf("finish reread: %v", err)
	}

	rows, err := database.Query(`
		SELECT to_status, reverted_at
		FROM user_work_reading_events
		WHERE user_id = ? AND work_id = ?
		ORDER BY seq ASC
	`, alice.ID, "w1")
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

	bobState, err := GetReadingStatus(database, bob.ID, "w1")
	if err != nil || bobState.Status != ReadingStatusUnread {
		t.Fatalf("bob status = %+v, err %v; alice state leaked", bobState, err)
	}
}

func TestAutomaticReadingStatusKeepsExplicitTerminalStates(t *testing.T) {
	database := newTestDB(t)
	user, err := database.CreateUser("reader", "pw", RoleReader)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO works (id, title, sort_title) VALUES ('w1', 'Book', 'Book');
		INSERT INTO assets (id, work_id, storage_path, filename, extension, koreader_hash)
		VALUES ('a1', 'w1', 'book.epub', 'book.epub', '.epub', 'known');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := database.SetReadingStatus(context.Background(), user.ID, "w1", ReadingStatusDropped, ReadingStatusSourceManual); err != nil {
		t.Fatalf("drop: %v", err)
	}
	change, err := database.AdvanceReadingStatusForDocumentHash(context.Background(), user.ID, "known", 1)
	if err != nil || change.Changed || change.State.Status != ReadingStatusDropped {
		t.Fatalf("KOSync changed dropped status: %+v, err %v", change, err)
	}
	unknown, err := database.AdvanceReadingStatusForDocumentHash(context.Background(), user.ID, "unknown", 0.5)
	if err != nil || unknown.Changed || unknown.State.Status != "" {
		t.Fatalf("unknown KOSync hash = %+v, err %v", unknown, err)
	}
}
