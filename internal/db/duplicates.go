package db

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/levmv/polka/internal/id"
)

const DuplicateReasonTitleAuthor = "title_author"

var ErrInvalidDuplicateGroup = errors.New("duplicate group is no longer valid")

// Duplicate detection is intentionally conservative: the current detector groups
// live works by normalized title + primary author only. GetPossibleDuplicates
// returns the total number of non-dismissed groups before maxGroups pagination,
// then materializes up to maxGroups of those groups for display.
type DuplicateGroup struct {
	Reason string
	Key    string
	Books  []BookSummaryRow
}

// GetPossibleDuplicates scans live works and groups normalized keys in Go
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

	var ids []string
	for _, key := range selectedKeys {
		ids = append(ids, groupIDs[key]...)
	}
	booksByID, err := bookSummariesByIDs(queryer, ids)
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
	SurvivorID  string
	WorkIDs     []string
	DeletedBy   string
	CoverFromID string
}

type DuplicateMergeResult struct {
	SurvivorID        string
	TrashedIDs        []string
	FilledDescription bool
	FilledCover       bool
}

type duplicateCandidate struct {
	id            string
	title         string
	primaryAuthor string
}

type duplicateWork struct {
	id            string
	title         string
	primaryAuthor string
	description   sql.NullString
	coverVersion  int
}

type duplicateSet struct {
	reason string
	key    string
	ids    []string
	works  map[string]duplicateWork
}

type duplicateReadingState struct {
	userID      string
	workID      string
	status      string
	lastEventID sql.NullString
	updatedAt   int64
}

func queryDuplicateCandidates(queryer Queryer, scope VisibilityScope) (*sql.Rows, error) {
	where, args := scope.AppendWorkWhere("w.deleted_at IS NULL", "w.id")
	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT w.id, w.title,
		       COALESCE(%s, '') as primary_author
		FROM works w
		WHERE %s
		ORDER BY w.added_at DESC
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

func duplicateCandidateGroups(queryer Queryer, scope VisibilityScope) ([]string, map[string][]string, error) {
	rows, err := queryDuplicateCandidates(queryer, scope)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	groups := make(map[string][]string)
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

func DuplicateMergeCoverSource(queryer Queryer, scope VisibilityScope, survivorID string, workIDs []string) (string, error) {
	set, err := validateDuplicateSet(queryer, scope, survivorID, workIDs)
	if err != nil {
		return "", err
	}

	survivor := set.works[survivorID]
	if survivor.coverVersion > 0 {
		return "", nil
	}
	for _, workID := range set.ids {
		if workID == survivorID {
			continue
		}
		if set.works[workID].coverVersion > 0 {
			return workID, nil
		}
	}
	return "", nil
}

// DismissDuplicateGroup records the exact live group the user dismissed. A
// later metadata edit or import changes the key/member set and should surface a
// fresh cleanup item; dismissals are not broad "never show this title again"
// suppressions.
func DismissDuplicateGroup(tx *sql.Tx, scope VisibilityScope, workIDs []string, userID string) error {
	set, err := validateDuplicateSet(tx, scope, "", workIDs)
	if err != nil {
		return err
	}
	ids := append([]string(nil), set.ids...)
	slices.Sort(ids)
	_, err = tx.Exec(`
		INSERT INTO duplicate_dismissals (id, reason, detector_key, work_ids, created_by)
		VALUES (?, ?, ?, ?, ?)
	`, id.New(id.DuplicateDismissal), set.reason, set.key, strings.Join(ids, "\n"), nullString(userID))
	if err != nil {
		return fmt.Errorf("insert duplicate dismissal: %w", err)
	}
	return nil
}

func MergeDuplicateWorks(tx *sql.Tx, scope VisibilityScope, req DuplicateMergeRequest) (DuplicateMergeResult, error) {
	set, err := validateDuplicateSet(tx, scope, req.SurvivorID, req.WorkIDs)
	if err != nil {
		return DuplicateMergeResult{}, err
	}

	loserIDs := make([]string, 0, len(set.ids)-1)
	for _, workID := range set.ids {
		if workID != req.SurvivorID {
			loserIDs = append(loserIDs, workID)
		}
	}
	if len(loserIDs) == 0 {
		return DuplicateMergeResult{}, ErrInvalidDuplicateGroup
	}

	result := DuplicateMergeResult{
		SurvivorID: req.SurvivorID,
		TrashedIDs: append([]string(nil), loserIDs...),
	}

	survivor := set.works[req.SurvivorID]
	if strings.TrimSpace(survivor.description.String) == "" {
		for _, workID := range loserIDs {
			desc := strings.TrimSpace(set.works[workID].description.String)
			if desc == "" {
				continue
			}
			if _, err := tx.Exec(`
				UPDATE works
				SET description = ?, updated_at = unixepoch()
				WHERE id = ?
			`, desc, req.SurvivorID); err != nil {
				return DuplicateMergeResult{}, fmt.Errorf("fill duplicate description: %w", err)
			}
			result.FilledDescription = true
			break
		}
	}

	if req.CoverFromID != "" {
		source, ok := set.works[req.CoverFromID]
		if !ok || req.CoverFromID == req.SurvivorID || source.coverVersion <= 0 || survivor.coverVersion > 0 {
			return DuplicateMergeResult{}, ErrInvalidDuplicateGroup
		}
		res, err := tx.Exec(`
			UPDATE works
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
		WHERE work_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("demote duplicate loser assets: %w", err)
	}

	args := append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		UPDATE assets
		SET work_id = ?, updated_at = unixepoch()
		WHERE work_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("move duplicate assets: %w", err)
	}

	if err := EnsureReadablePrimaryAsset(tx, req.SurvivorID); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("ensure duplicate survivor primary asset: %w", err)
	}

	args = append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO shelf_books (shelf_id, work_id, position, added_at)
		SELECT shelf_id, ?, position, added_at
		FROM shelf_books
		WHERE work_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("merge duplicate shelf memberships: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM shelf_books
		WHERE work_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("delete duplicate loser shelf memberships: %w", err)
	}

	args = append([]any{req.SurvivorID}, loserArgs...)
	if _, err := tx.Exec(`
		UPDATE delivery_jobs
		SET work_id = ?, updated_at = unixepoch()
		WHERE work_id IN (`+loserPlaceholders+`)
	`, args...); err != nil {
		return DuplicateMergeResult{}, fmt.Errorf("merge duplicate delivery jobs: %w", err)
	}

	if err := mergeDuplicateReadingData(tx, req.SurvivorID, loserIDs); err != nil {
		return DuplicateMergeResult{}, err
	}

	for _, workID := range loserIDs {
		if err := SoftDeleteWork(tx, workID, req.DeletedBy); err != nil {
			return DuplicateMergeResult{}, fmt.Errorf("trash duplicate loser %s: %w", workID, err)
		}
	}
	return result, nil
}

// mergeDuplicateReadingData retains every event history while choosing one
// current state per user. The newest updated state wins; the selected survivor
// wins an exact timestamp tie, followed by stable work-id order. Event chains
// carry explicit predecessors, so moving independent histories onto one work
// does not make Undo jump from the selected history into another one.
func mergeDuplicateReadingData(tx *sql.Tx, survivorID string, loserIDs []string) error {
	workIDs := append([]string{survivorID}, loserIDs...)
	placeholders, args := idPlaceholders(workIDs)
	rows, err := tx.Query(`
		SELECT user_id, work_id, status, last_event_id, updated_at
		FROM user_work_reading_state
		WHERE work_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return fmt.Errorf("query duplicate reading states: %w", err)
	}

	winners := make(map[string]duplicateReadingState)
	for rows.Next() {
		var candidate duplicateReadingState
		if err := rows.Scan(&candidate.userID, &candidate.workID, &candidate.status, &candidate.lastEventID, &candidate.updatedAt); err != nil {
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
		UPDATE user_work_reading_events
		SET work_id = ?
		WHERE work_id IN (`+loserPlaceholders+`)
	`, eventArgs...); err != nil {
		return fmt.Errorf("merge duplicate reading events: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM user_work_reading_state
		WHERE work_id IN (`+loserPlaceholders+`)
	`, loserArgs...); err != nil {
		return fmt.Errorf("delete duplicate loser reading states: %w", err)
	}

	for _, state := range winners {
		if _, err := tx.Exec(`
			INSERT INTO user_work_reading_state
				(user_id, work_id, status, last_event_id, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(user_id, work_id) DO UPDATE SET
				status = excluded.status,
				last_event_id = excluded.last_event_id,
				updated_at = excluded.updated_at
		`, state.userID, survivorID, state.status, state.lastEventID, state.updatedAt); err != nil {
			return fmt.Errorf("merge duplicate reading state for user %s: %w", state.userID, err)
		}
	}
	return nil
}

func preferDuplicateReadingState(candidate, current duplicateReadingState, survivorID string) bool {
	if candidate.updatedAt != current.updatedAt {
		return candidate.updatedAt > current.updatedAt
	}
	if candidate.workID == survivorID || current.workID == survivorID {
		return candidate.workID == survivorID
	}
	return candidate.workID < current.workID
}

// validateDuplicateSet re-checks the detector contract at mutation time so a
// stale UI cannot merge/dismiss arbitrary work IDs. Every supplied live work
// must still be visible in scope and must still share the same detector key.
func validateDuplicateSet(q Queryer, scope VisibilityScope, survivorID string, workIDs []string) (duplicateSet, error) {
	ids := dedupWorkIDs(workIDs)
	if len(ids) < 2 {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}
	if survivorID != "" && !slices.Contains(ids, survivorID) {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}

	works, err := duplicateWorksForIDs(q, scope, ids)
	if err != nil {
		return duplicateSet{}, err
	}
	if len(works) != len(ids) {
		return duplicateSet{}, ErrInvalidDuplicateGroup
	}

	var key string
	for _, workID := range ids {
		work, ok := works[workID]
		if !ok {
			return duplicateSet{}, ErrInvalidDuplicateGroup
		}
		workKey := duplicateMatchKey(work.title, work.primaryAuthor)
		if key == "" {
			key = workKey
			continue
		}
		if workKey != key {
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
		works:  works,
	}, nil
}

func duplicateWorksForIDs(queryer Queryer, scope VisibilityScope, ids []string) (map[string]duplicateWork, error) {
	placeholders, args := idPlaceholders(ids)
	where := "w.deleted_at IS NULL AND w.id IN (" + placeholders + ")"
	where, args = scope.AppendWorkWhere(where, "w.id", args...)

	rows, err := queryer.Query(fmt.Sprintf(`
		SELECT w.id, w.title,
		       COALESCE(%s, '') AS primary_author,
		       w.description, w.cover_version
		FROM works w
		WHERE %s
	`, subPrimaryAuthorName, where), args...)
	if err != nil {
		return nil, fmt.Errorf("query duplicate works: %w", err)
	}
	defer rows.Close()

	works := make(map[string]duplicateWork, len(ids))
	for rows.Next() {
		var work duplicateWork
		if err := rows.Scan(&work.id, &work.title, &work.primaryAuthor, &work.description, &work.coverVersion); err != nil {
			return nil, fmt.Errorf("scan duplicate work: %w", err)
		}
		works[work.id] = work
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duplicate work rows: %w", err)
	}
	return works, nil
}

type duplicateDismissal struct {
	ids map[string]struct{}
}

func duplicateDismissals(queryer Queryer) (map[string][]duplicateDismissal, error) {
	rows, err := queryer.Query(`
		SELECT reason, detector_key, work_ids
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
		d := duplicateDismissal{
			ids: make(map[string]struct{}),
		}
		for id := range strings.SplitSeq(rawIDs, "\n") {
			id = strings.TrimSpace(id)
			if id != "" {
				d.ids[id] = struct{}{}
			}
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
func duplicateGroupDismissed(dismissals map[string][]duplicateDismissal, reason, key string, ids []string) bool {
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

func dedupWorkIDs(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func nullString(s string) sql.NullString {
	if strings.TrimSpace(s) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func bookSummariesByIDs(queryer Queryer, ids []string) (map[string]BookSummaryRow, error) {
	books := make(map[string]BookSummaryRow, len(ids))
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		chunk := ids[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}

		rows, err := queryer.Query(fmt.Sprintf(`
			SELECT %s
			FROM works w
			WHERE w.id IN (`+placeholders+`)
		`, bookSummaryColumns), args...)
		if err != nil {
			return nil, fmt.Errorf("query duplicate summaries: %w", err)
		}
		for rows.Next() {
			book, err := scanBookSummary(rows)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan duplicate summary: %w", err)
			}
			books[book.ID] = book
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("duplicate summary rows: %w", err)
		}
		rows.Close()
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
