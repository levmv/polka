package db

import "testing"

func TestBookIdentitySurvivesPurgeAndVacuum(t *testing.T) {
	database := newTestDB(t)
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		if _, err := tx.Exec(`INSERT INTO books (id, title, sort_title, deleted_at) VALUES
			(5, 'Retained', 'Retained', NULL), (21, 'Removed', 'Removed', 100)`); err != nil {
			return err
		}
		// Index in reverse order: FTS addresses must come from the catalog ID.
		for _, id := range []int64{21, 5} {
			if err := UpdateSearchIndex(tx, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		return PurgeBook(tx, 21)
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, database, "VACUUM")

	if books := mustListBooks(t, database, "Retained"); len(books) != 1 || books[0].ID != 5 {
		t.Fatalf("search after vacuum = %+v; want book 5", books)
	}

	// Removing the highest committed ID must not let a new book inherit it.
	var nextID int64
	if err := database.Transact(t.Context(), func(tx *Tx) error {
		result, err := tx.Exec("INSERT INTO books (title, sort_title) VALUES ('NewArrival', 'NewArrival')")
		if err != nil {
			return err
		}
		nextID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		return UpdateSearchIndex(tx, nextID)
	}); err != nil {
		t.Fatal(err)
	}
	if nextID <= 21 {
		t.Fatalf("deleted book identity reused: %d", nextID)
	}
	if books := mustListBooks(t, database, "NewArrival"); len(books) != 1 || books[0].ID != nextID {
		t.Fatalf("search after insert = %+v; want book %d", books, nextID)
	}
}
