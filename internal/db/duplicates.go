package db

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

const DuplicateReasonTitleAuthor = "title_author"

var ErrInvalidDuplicateGroup = errors.New("duplicate group is no longer valid")

// Duplicate detection is intentionally conservative: the current detector groups
// live books by normalized title + primary author only. GetPossibleDuplicates
// returns the total number of non-dismissed groups before maxGroups pagination,
// then materializes up to maxGroups of those groups for display.
type DuplicateGroup struct {
	Reason string
	Key    string
	Books  []BookSummaryRow
}

// GetPossibleDuplicates scans live books and groups normalized keys in Go
// rather than in SQL — exact SQL grouping would need a denormalized
// Unicode-aware dedup key column. Deliberate; revisit only if duplicate
// suggestions prove hot on a large real library, not as a drive-by rewrite.
func GetPossibleDuplicates(queryer Queryer, scope VisibilityScope, maxGroups int) (int, []DuplicateGroup, error) {
	keys, groupIDs, err := duplicateCandidateGroups(queryer, scope)
	if err != nil {
		return 0, nil, err
	}

	dismissals, err := duplicateDismissals(queryer)
	if err != nil {
		return 0, nil, err
	}

	var selectedKeys []string
	count := 0
	for _, key := range keys {
		ids := groupIDs[key]
		if len(ids) <= 1 {
			continue
		}
		if duplicateGroupDismissed(dismissals, DuplicateReasonTitleAuthor, key, ids) {
			continue
		}
		count++
		if maxGroups > 0 && len(selectedKeys) < maxGroups {
			selectedKeys = append(selectedKeys, key)
		}
	}
	if len(selectedKeys) == 0 {
		return count, nil, nil
	}

	var ids []int64
	for _, key := range selectedKeys {
		ids = append(ids, groupIDs[key]...)
	}
	booksByID, err := bookSummariesByIDs(queryer, scope, ids)
	if err != nil {
		return 0, nil, err
	}

	duplicateGroups := make([]DuplicateGroup, 0, len(selectedKeys))
	for _, key := range selectedKeys {
		books := make([]BookSummaryRow, 0, len(groupIDs[key]))
		for _, id := range groupIDs[key] {
			if book, ok := booksByID[id]; ok {
				books = append(books, book)
			}
		}
		duplicateGroups = append(duplicateGroups, DuplicateGroup{
			Reason: DuplicateReasonTitleAuthor,
			Key:    key,
			Books:  books,
		})
	}

	return count, duplicateGroups, nil
}

type DuplicateMergeRequest struct {
	SurvivorID  int64
	BookIDs     []int64
	DeletedBy   int64
	CoverFromID int64
}

type DuplicateMergeResult struct {
	SurvivorID        int64
	TrashedIDs        []int64
	FilledDescription bool
	FilledCover       bool
}

type duplicateCandidate struct {
	id            int64
	title         string
	primaryAuthor string
}

type duplicateBook struct {
	id            int64
	title         string
	primaryAuthor string
	description   sql.NullString
	coverVersion  int
}

type duplicateSet struct {
	reason string
	key    string
	ids    []int64
	books  map[int64]duplicateBook
}

type duplicateReadingState struct {
	userID      int64
	bookID      int64
	status      string
	lastEventID sql.NullInt64
	updatedAt   int64
}

func queryDuplicateCandidates(queryer Queryer, scope VisibilityScope) (*sql.Rows, error) {
	where, args := scope.AppendBookWhere("b.deleted_at IS NULL", "b.id")
	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT b.id, b.title,
		       COALESCE(%s, '') as primary_author
		FROM books b
		WHERE %s
		ORDER BY b.added_at DESC
	`, subPrimaryAuthorName, where), args...)
	if err != nil {
		return nil, fmt.Errorf("query duplicate candidates: %w", err)
	}
	return rows, nil
}

func scanDuplicateCandidate(rows *sql.Rows) (duplicateCandidate, error) {
	var c duplicateCandidate
	if err := rows.Scan(&c.id, &c.title, &c.primaryAuthor); err != nil {
		return duplicateCandidate{}, err
	}
	return c, nil
}

func duplicateCandidateGroups(queryer Queryer, scope VisibilityScope) ([]string, map[string][]int64, error) {
	rows, err := queryDuplicateCandidates(queryer, scope)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	groups := make(map[string][]int64)
	var keys []string
	for rows.Next() {
		c, err := scanDuplicateCandidate(rows)
		if err != nil {
			return nil, nil, fmt.Errorf("scan duplicate candidate: %w", err)
		}
		key := duplicateMatchKey(c.title, c.primaryAuthor)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], c.id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("duplicate candidate rows: %w", err)
	}
	return keys, groups, nil
}

func DuplicateMergeCoverSource(queryer Queryer, scope VisibilityScope, survivorID int64, bookIDs []int64) (int64, error) {
	set, err := validateDuplicateSet(queryer, scope, survivorID, bookIDs)
	if err != nil {
		return 0, err
	}

	survivor := set.books[survivorID]
	if survivor.coverVersion > 0 {
		return 0, nil
	}
	for _, bookID := range set.ids {
		if bookID == survivorID {
			continue
		}
		if set.books[bookID].coverVersion > 0 {
			return bookID, nil
		}
	}
	return 0, nil
}

// DismissDuplicateGroup records the exact live group the user dismissed. A
// later metadata edit or import changes the key/member set and should surface a
// fresh cleanup item; dismissals are not broad "never show this title again"
// suppressions.
func DismissDuplicateGroup(tx *Tx, scope VisibilityScope, bookIDs []int64, userID int64) error {
	set, err := validateDuplicateSet(tx, scope, 0, bookIDs)
	if err != nil {
		return err
	}
	// Store the same representation regardless of selection order.
	slices.Sort(set.ids)
	encodedIDs, err := json.Marshal(set.ids)
	if err != nil {
		return fmt.Errorf("encode dismissed book IDs: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO duplicate_dismissals (reason, detector_key, book_ids, created_by)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(reason, detector_key, book_ids) DO NOTHING
	`, set.reason, set.key, string(encodedIDs), sql.NullInt64{Int64: userID, Valid: userID > 0})
	if err != nil {
		return fmt.Errorf("insert duplicate dismissal: %w", err)
	}
	return nil
}

func MergeDuplicateBooks(tx *Tx, scope VisibilityScope, req DuplicateMergeRequest) (DuplicateMergeResult, error) {
	set, err := validateDuplicateSet(tx, scope, req.SurvivorID, req.BookIDs)
	if err != nil {
		return DuplicateMergeResult{}, err
	}

	loserIDs := make([]int64, 0, len(set.ids)-1)
	for _, bookID := range set.ids {
		if bookID != req.SurvivorID {
			loserIDs = append(loserIDs, bookID)
		}
	}
	if len(loserIDs) == 0 {
		return DuplicateMergeResult{}, ErrInvalidDuplicateGroup
	}

	result := DuplicateMergeResult{
		SurvivorID: req.SurvivorID,
		TrashedIDs: append([]int64(nil), loserIDs...),
	}

	survivor := set.books[req.SurvivorID]
	if strings.TrimSpace(survivor.description.String) == "" {
		for _, bookID := range loserIDs {
			desc := strings.TrimSpace(set.books[bookID].description.String)
			if desc == "" {
				continue
			}
			if _, err := tx.Exec(`
				UPDATE books
				SET description = ?, updated_at = unixepoch()
				WHERE id = ?
			`, desc, req.SurvivorID); err != nil {
				return DuplicateMergeResult{}, fmt.Errorf("fill duplicate description: %w", err)
			}
			result.FilledDescription = true
			break
		}
	}

	if req.CoverFromID != 0 {
		source, ok := set.books[req.CoverFromID]
		if !ok || req.CoverFromID == req.SurvivorID || source.coverVersion <= 0 || survivor.coverVersion > 0 {
			return DuplicateMergeResult{}, ErrInvalidDuplicateGroup
		}
		res, err := tx.Exec(`
			UPDATE books
			SET cover_version = cover_version + 1, updated_at = unixepoch()
			WHERE id = ? AND cover_version <= 0
		`, req.SurvivorID)
		if err != nil {
			return DuplicateMergeResult{}, fmt.Errorf("fill duplicate cover: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return DuplicateMergeResult{}, ErrInvalidDuplicateGroup
		}
		result.FilledCover = true
	}

	loserPlaceholders, loserArgs := idPlaceholders(loserIDs)

	if _, err := tx.Exec(`
		UPDATE assets
		SET is_primary = 0, updated_at = unixepoch()
		WHERE book_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("demote duplicate loser assets: %w", err)
	}

	// Revisions belong to one book; acknowledgement from the former book says
	// nothing about whether these files contain the survivor's metadata.
	// Pending writes must retain their hashes for byte recovery, but repair must
	// not restore acknowledgement of the former book's revision either.
	if _, err := tx.Exec(`
		UPDATE metadata_writeback_attempts
		SET metadata_rev = 0
		WHERE asset_id IN (SELECT id FROM assets WHERE book_id IN (`+loserPlaceholders+`))
	`, loserArgs...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("invalidate duplicate writeback attempts: %w", err)
	}

	args := append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		UPDATE assets
		SET book_id = ?, writeback_rev = 0, writeback_error = NULL, updated_at = unixepoch()
		WHERE book_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("move duplicate assets: %w", err)
	}

	if err := EnsureReadablePrimaryAsset(tx, req.SurvivorID); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("ensure duplicate survivor primary asset: %w", err)
	}

	args = append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO shelf_books (shelf_id, book_id, position, added_at)
		SELECT shelf_id, ?, position, added_at
		FROM shelf_books
		WHERE book_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("merge duplicate shelf memberships: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM shelf_books
		WHERE book_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("delete duplicate loser shelf memberships: %w", err)
	}

	args = append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		UPDATE delivery_jobs
		SET book_id = ?, updated_at = unixepoch()
		WHERE book_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("merge duplicate delivery jobs: %w", err)
	}

	if err := mergeDuplicateReadingData(tx, req.SurvivorID, loserIDs); err != nil {
		return DuplicateMergeResult{}, err
	}

	for _, bookID := range loserIDs {
		if err := SoftDeleteBook(tx, bookID, req.DeletedBy); err != nil {
			return DuplicateMergeResult{}, fmt.Errorf("trash duplicate loser %d: %w", bookID, err)
		}
	}
	return result, nil
}

// mergeDuplicateReadingData retains every event history while choosing one
// current state per user. The newest updated state wins; the selected survivor
// wins an exact timestamp tie, followed by stable book-id order. Event chains
// carry explicit predecessors, so moving independent histories onto one book
// does not make Undo jump from the selected history into another one.
func mergeDuplicateReadingData(tx *Tx, survivorID int64, loserIDs []int64) error {
	bookIDs := append([]int64{survivorID}, loserIDs...)
	placeholders, args := idPlaceholders(bookIDs)
	rows, err := tx.Query(`
		SELECT user_id, book_id, status, last_event_id, updated_at
		FROM user_book_reading_state
		WHERE book_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("query duplicate reading states: %w", err)
	}

	winners := make(map[int64]duplicateReadingState)
	for rows.Next() {
		var candidate duplicateReadingState
		if err := rows.Scan(&candidate.userID, &candidate.bookID, &candidate.status, &candidate.lastEventID, &candidate.updatedAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan duplicate reading state: %w", err)
		}
		current, ok := winners[candidate.userID]
		if !ok || preferDuplicateReadingState(candidate, current, survivorID) {
			winners[candidate.userID] = candidate
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("duplicate reading state rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close duplicate reading states: %w", err)
	}

	loserPlaceholders, loserArgs := idPlaceholders(loserIDs)
	eventArgs := append([]any{survivorID}, loserArgs...)
	if _, err := tx.Exec(`
		UPDATE user_book_reading_events
		SET book_id = ?
		WHERE book_id IN (`+loserPlaceholders+`)
	`, eventArgs...); err != nil {
		return fmt.Errorf("merge duplicate reading events: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM user_book_reading_state
		WHERE book_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return fmt.Errorf("delete duplicate loser reading states: %w", err)
	}

	for _, state := range winners {
		if _, err := tx.Exec(`
			INSERT INTO user_book_reading_state
				(user_id, book_id, status, last_event_id, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(user_id, book_id) DO UPDATE SET
				status = excluded.status,
				last_event_id = excluded.last_event_id,
				updated_at = excluded.updated_at
		`, state.userID, survivorID, state.status, state.lastEventID, state.updatedAt); err != nil {
			return fmt.Errorf("merge duplicate reading state for user %d: %w", state.userID, err)
		}
	}
	return nil
}

func preferDuplicateReadingState(candidate, current duplicateReadingState, survivorID int64) bool {
	if candidate.updatedAt != current.updatedAt {
		return candidate.updatedAt > current.updatedAt
	}
	if candidate.bookID == survivorID || current.bookID == survivorID {
		return candidate.bookID == survivorID
	}
	return candidate.bookID < current.bookID
}

// validateDuplicateSet re-checks the detector contract at mutation time so a
// stale UI cannot merge/dismiss arbitrary book IDs. Every supplied live book
// must still be visible in scope and must still share the same detector key.
func validateDuplicateSet(q Queryer, scope VisibilityScope, survivorID int64, bookIDs []int64) (duplicateSet, error) {
	ids := DedupBookIDs(bookIDs)
	if len(ids) < 2 {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}
	if survivorID != 0 && !slices.Contains(ids, survivorID) {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}

	books, err := duplicateBooksForIDs(q, scope, ids)
	if err != nil {
		return duplicateSet{}, err
	}
	if len(books) != len(ids) {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}

	var key string
	for _, bookID := range ids {
		book, ok := books[bookID]
		if !ok {
			return duplicateSet{}, ErrInvalidDuplicateGroup
		}
		bookKey := duplicateMatchKey(book.title, book.primaryAuthor)
		if key == "" {
			key = bookKey
			continue
		}
		if bookKey != key {
			return duplicateSet{}, ErrInvalidDuplicateGroup
		}
	}
	if key == "" {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}
	return duplicateSet{
		reason: DuplicateReasonTitleAuthor,
		key:    key,
		ids:    ids,
		books:  books,
	}, nil
}

func duplicateBooksForIDs(queryer Queryer, scope VisibilityScope, ids []int64) (map[int64]duplicateBook, error) {
	placeholders, args := idPlaceholders(ids)
	where := "b.deleted_at IS NULL AND b.id IN (" + placeholders + ")"
	where, args = scope.AppendBookWhere(where, "b.id", args...)

	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT b.id, b.title,
		       COALESCE(%s, '') AS primary_author,
		       b.description, b.cover_version
		FROM books b
		WHERE %s
	`, subPrimaryAuthorName, where), args...)
	if err != nil {
		return nil, fmt.Errorf("query duplicate books: %w", err)
	}
	defer rows.Close()

	books := make(map[int64]duplicateBook, len(ids))
	for rows.Next() {
		var book duplicateBook
		if err := rows.Scan(&book.id, &book.title, &book.primaryAuthor, &book.description, &book.coverVersion); err != nil {
			return nil, fmt.Errorf("scan duplicate book: %w", err)
		}
		books[book.id] = book
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duplicate book rows: %w", err)
	}
	return books, nil
}

type duplicateDismissal struct {
	ids map[int64]struct{}
}

func duplicateDismissals(queryer Queryer) (map[string][]duplicateDismissal, error) {
	rows, err := queryer.Query(`
		SELECT reason, detector_key, book_ids
		FROM duplicate_dismissals
	`)
	if err != nil {
		return nil, fmt.Errorf("query duplicate dismissals: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]duplicateDismissal)
	for rows.Next() {
		var reason, key, rawIDs string
		if err := rows.Scan(&reason, &key, &rawIDs); err != nil {
			return nil, fmt.Errorf("scan duplicate dismissal: %w", err)
		}
		var ids []int64
		if err := json.Unmarshal([]byte(rawIDs), &ids); err != nil {
			return nil, fmt.Errorf("decode dismissed book IDs: %w", err)
		}
		d := duplicateDismissal{ids: make(map[int64]struct{}, len(ids))}
		for _, bookID := range ids {
			d.ids[bookID] = struct{}{}
		}
		out[duplicateDismissalMapKey(reason, key)] = append(out[duplicateDismissalMapKey(reason, key)], d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duplicate dismissal rows: %w", err)
	}
	return out, nil
}

// duplicateGroupDismissed hides a group only when one dismissal covers the
// current live member set for the same detector key. Supersets count so a
// dismissed group can shrink after a member is trashed without reappearing; new
// imports/metadata edits produce a different uncovered member set or key.
func duplicateGroupDismissed(dismissals map[string][]duplicateDismissal, reason, key string, ids []int64) bool {
	for _, d := range dismissals[duplicateDismissalMapKey(reason, key)] {
		if len(d.ids) < len(ids) {
			continue
		}
		covered := true
		for _, id := range ids {
			if _, ok := d.ids[id]; !ok {
				covered = false
				break
			}
		}
		if covered {
			return true
		}
	}
	return false
}

func duplicateDismissalMapKey(reason, key string) string {
	return reason + "\x00" + key
}

func bookSummariesByIDs(queryer Queryer, scope VisibilityScope, ids []int64) (map[int64]BookSummaryRow, error) {
	books := make(map[int64]BookSummaryRow, len(ids))
	for chunk := range slices.Chunk(ids, 500) {
		rows, err := BookSummaryRowsByIDs(queryer, scope, chunk)
		if err != nil {
			return nil, fmt.Errorf("query duplicate summaries: %w", err)
		}
		for _, book := range rows {
			books[book.ID] = book
		}
	}
	return books, nil
}

// duplicateMatchKey produces the title+primary-author key for the first-pass
// duplicate detector. It lowercases, strips punctuation/symbols, keeps Unicode
// letters and digits, and collapses whitespace. It deliberately does not fold
// diacritics or use identifiers here; stronger detectors should be added as new
// reasons so dismissals and explanations stay precise.
func duplicateMatchKey(title, author string) string {
	var key strings.Builder
	key.Grow(len(title) + 1 + len(author))
	writeDuplicateKeyPart(&key, title)
	key.WriteByte('|')
	writeDuplicateKeyPart(&key, author)
	return key.String()
}

func writeDuplicateKeyPart(key *strings.Builder, value string) {
	wrote := false
	pendingSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSpace && wrote {
				key.WriteByte(' ')
			}
			key.WriteRune(unicode.ToLower(r))
			wrote = true
			pendingSpace = false
		} else if unicode.IsSpace(r) && wrote {
			pendingSpace = true
		}
	}
}
