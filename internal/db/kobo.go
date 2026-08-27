package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/levmv/polka/internal/id"
)

const KoboSyncPageLimit = 100

var (
	ErrKoboConnectionNotFound = errors.New("kobo connection not found")
	ErrKoboInvalidCursor      = errors.New("invalid kobo sync cursor")
)

type KoboConnection struct {
	ID         string
	UserID     string
	ShelfID    string
	ShelfName  string
	Revision   int64
	CreatedAt  int64
	UpdatedAt  int64
	LastUsedAt sql.NullInt64
}

// KoboPublication is the complete server-side read model needed by the native
// Kobo DTO mapper. Fingerprint is deliberately omitted from API output; it only
// decides when the durable projection needs a new revision.
type KoboPublication struct {
	AssetID       string
	WorkID        string
	Format        string
	Size          int64
	Title         string
	Description   string
	Publisher     string
	PublishedDate string
	Language      string
	Series        string
	SeriesIndex   sql.NullFloat64
	Authors       []string
	AddedAt       int64
	ModifiedAt    int64
}

type KoboChange struct {
	KoboPublication
	Revision      int64
	FirstRevision int64
	Present       bool
	ChangedAt     int64
}

type koboCandidate struct {
	KoboPublication
	Fingerprint string
}

func koboTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newKoboToken() string {
	buf := make([]byte, 24)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// ReplaceKoboConnection creates a fresh URL credential for one selected shelf.
// Replacing instead of editing makes both revocation and a shelf change atomic:
// the old token and its projection disappear in the same transaction.
func (db *DB) ReplaceKoboConnection(ctx context.Context, userID, shelfID string) (*KoboConnection, string, error) {
	shelf, err := db.GetShelfForUser(shelfID, userID)
	if err != nil {
		return nil, "", err
	}
	token := newKoboToken()
	connectionID := id.New(id.KoboConnection)

	err = db.Transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM kobo_connections WHERE user_id = ?", userID); err != nil {
			return fmt.Errorf("replace kobo connection: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO kobo_connections (id, user_id, shelf_id, token_hash)
			VALUES (?, ?, ?, ?)
		`, connectionID, userID, shelf.ID, koboTokenHash(token)); err != nil {
			return fmt.Errorf("insert kobo connection: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	connection, err := db.KoboConnectionForUser(userID)
	if err != nil {
		return nil, "", err
	}
	return connection, token, nil
}

func (db *DB) KoboConnectionForUser(userID string) (*KoboConnection, error) {
	return scanKoboConnection(db.QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, s.name, kc.revision,
		       kc.created_at, kc.updated_at, kc.last_used_at
		FROM kobo_connections kc
		JOIN shelves s ON s.id = kc.shelf_id
		WHERE kc.user_id = ?
	`, userID))
}

func scanKoboConnection(row *sql.Row) (*KoboConnection, error) {
	var connection KoboConnection
	err := row.Scan(
		&connection.ID, &connection.UserID, &connection.ShelfID, &connection.ShelfName,
		&connection.Revision, &connection.CreatedAt, &connection.UpdatedAt, &connection.LastUsedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKoboConnectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get kobo connection: %w", err)
	}
	return &connection, nil
}

func (db *DB) DeleteKoboConnection(userID string) error {
	result, err := db.Exec("DELETE FROM kobo_connections WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete kobo connection: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrKoboConnectionNotFound
	}
	return nil
}

// KoboConnectionByToken authenticates a Kobo URL without retaining its secret.
// The last-used timestamp is throttled so cover and download traffic does not
// turn every request into a database write.
func (db *DB) KoboConnectionByToken(token string) (*KoboConnection, bool, error) {
	if token == "" {
		return nil, false, nil
	}
	connection, err := scanKoboConnection(db.QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, s.name, kc.revision,
		       kc.created_at, kc.updated_at, kc.last_used_at
		FROM kobo_connections kc
		JOIN shelves s ON s.id = kc.shelf_id
		WHERE kc.token_hash = ?
	`, koboTokenHash(token)))
	if errors.Is(err, ErrKoboConnectionNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	now := time.Now().Unix()
	if !connection.LastUsedAt.Valid || now-connection.LastUsedAt.Int64 >= 3600 {
		if _, err := db.Exec(`
			UPDATE kobo_connections SET last_used_at = ? WHERE id = ?
		`, now, connection.ID); err != nil {
			return nil, false, fmt.Errorf("bump kobo connection: %w", err)
		}
		connection.LastUsedAt = sql.NullInt64{Int64: now, Valid: true}
	}
	return connection, true, nil
}

// SyncKoboConnection reconciles the selected shelf and reads one stable revision
// page in the same transaction. A retry with the same cursor therefore returns
// the same logical page unless real source data changed in between.
func (db *DB) SyncKoboConnection(ctx context.Context, connectionID string, after int64, limit int) ([]KoboChange, int64, bool, error) {
	if after < 0 || limit < 1 || limit > KoboSyncPageLimit {
		return nil, 0, false, ErrKoboInvalidCursor
	}

	var changes []KoboChange
	var currentRevision int64
	var more bool
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		connection, shelf, scope, err := loadKoboSyncState(tx, connectionID)
		if err != nil {
			return err
		}
		currentRevision, err = reconcileKoboItems(ctx, tx, connection, shelf, scope)
		if err != nil {
			return err
		}
		if after > currentRevision {
			return ErrKoboInvalidCursor
		}
		changes, more, err = listKoboChanges(ctx, tx, connectionID, after, limit)
		return err
	})
	if err != nil {
		return nil, 0, false, err
	}
	return changes, currentRevision, more, nil
}

func loadKoboSyncState(tx *sql.Tx, connectionID string) (*KoboConnection, *Shelf, VisibilityScope, error) {
	var connection KoboConnection
	var shelf Shelf
	var kind, role, contentScope string
	var query, queryMatch sql.NullString
	err := tx.QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, kc.revision,
		       s.name, s.kind, s.query, s.query_match,
		       u.role, u.content_scope
		FROM kobo_connections kc
		JOIN shelves s ON s.id = kc.shelf_id
		JOIN users u ON u.id = kc.user_id
		WHERE kc.id = ?
	`, connectionID).Scan(
		&connection.ID, &connection.UserID, &connection.ShelfID, &connection.Revision,
		&shelf.Name, &kind, &query, &queryMatch, &role, &contentScope,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, VisibilityScope{}, ErrKoboConnectionNotFound
	}
	if err != nil {
		return nil, nil, VisibilityScope{}, fmt.Errorf("get kobo sync context: %w", err)
	}
	shelf.ID = connection.ShelfID
	shelf.Kind = ShelfKind(kind)
	shelf.Query = query.String
	shelf.QueryMatch = queryMatch.String
	connection.ShelfName = shelf.Name

	scope := FullVisibilityScope()
	if role == RoleReader && contentScope == ContentScopeShelves {
		scope = VisibilityScope{UserID: connection.UserID, ContentScope: ContentScopeShelves}
	}
	return &connection, &shelf, scope, nil
}

func reconcileKoboItems(ctx context.Context, tx *sql.Tx, connection *KoboConnection, shelf *Shelf, scope VisibilityScope) (int64, error) {
	type existingItem struct {
		Fingerprint string
		Present     bool
	}
	existing := make(map[string]existingItem)
	rows, err := tx.QueryContext(ctx, `
		SELECT asset_id, fingerprint, present
		FROM kobo_items
		WHERE connection_id = ?
	`, connection.ID)
	if err != nil {
		return 0, fmt.Errorf("list current kobo items: %w", err)
	}
	for rows.Next() {
		var assetID, fingerprint string
		var present bool
		if err := rows.Scan(&assetID, &fingerprint, &present); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan current kobo item: %w", err)
		}
		existing[assetID] = existingItem{Fingerprint: fingerprint, Present: present}
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close current kobo items: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate current kobo items: %w", err)
	}

	// A repeat sync normally has one projection row per current candidate. That
	// gives the candidate slice a free, accurate capacity hint and avoids its
	// repeated growth. A brand-new connection still starts naturally at zero.
	candidates, err := listKoboCandidates(ctx, tx, connection.UserID, shelf, scope, len(existing))
	if err != nil {
		return 0, err
	}

	revision := connection.Revision
	for _, candidate := range candidates {
		old, found := existing[candidate.AssetID]
		// Once a current candidate has consumed its previous projection row,
		// anything left in existing is either an old tombstone or a removal.
		// This keeps one map instead of duplicating every selected asset ID in a
		// second desired set during each reconciliation.
		delete(existing, candidate.AssetID)
		if found && old.Present && old.Fingerprint == candidate.Fingerprint {
			continue
		}
		revision++
		if !found {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO kobo_items
				    (connection_id, asset_id, work_id, fingerprint, present, revision, first_revision)
				VALUES (?, ?, ?, ?, 1, ?, ?)
			`, connection.ID, candidate.AssetID, candidate.WorkID, candidate.Fingerprint, revision, revision); err != nil {
				return 0, fmt.Errorf("insert kobo item: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE kobo_items
			SET work_id = ?, fingerprint = ?, present = 1, revision = ?, updated_at = unixepoch()
			WHERE connection_id = ? AND asset_id = ?
		`, candidate.WorkID, candidate.Fingerprint, revision, connection.ID, candidate.AssetID); err != nil {
			return 0, fmt.Errorf("update kobo item: %w", err)
		}
	}

	var removals []string
	for assetID, item := range existing {
		if item.Present {
			removals = append(removals, assetID)
		}
	}
	slices.Sort(removals)
	for _, assetID := range removals {
		revision++
		if _, err := tx.ExecContext(ctx, `
			UPDATE kobo_items
			SET present = 0, revision = ?, updated_at = unixepoch()
			WHERE connection_id = ? AND asset_id = ?
		`, revision, connection.ID, assetID); err != nil {
			return 0, fmt.Errorf("tombstone kobo item: %w", err)
		}
	}

	if revision != connection.Revision {
		if _, err := tx.ExecContext(ctx, `
			UPDATE kobo_connections SET revision = ?, updated_at = unixepoch() WHERE id = ?
		`, revision, connection.ID); err != nil {
			return 0, fmt.Errorf("advance kobo revision: %w", err)
		}
	}
	return revision, nil
}

func listKoboCandidates(ctx context.Context, tx *sql.Tx, userID string, shelf *Shelf, scope VisibilityScope, capacityHint int) ([]koboCandidate, error) {
	var withSQL, fromSQL, whereSQL string
	var args []any

	if shelf.Kind == ShelfManual {
		joined := "shelf_books sb JOIN works w ON w.id = sb.work_id JOIN assets a ON a.work_id = w.id"
		withSQL, fromSQL, args = scope.joinVisibleWorks(joined)
		whereSQL = "w.deleted_at IS NULL AND a.format IN ('kepub', 'epub') AND sb.shelf_id = ?"
		args = append(args, shelf.ID)
	} else {
		plan := newBookSearchPlan(scope, userID, shelf.Query)
		if !plan.hasClauses {
			return nil, nil
		}
		withSQL = plan.withSQL
		fromSQL = plan.fromSQL + " JOIN assets a ON a.work_id = w.id"
		whereSQL = plan.whereSQL + " AND a.format IN ('kepub', 'epub')"
		args = plan.argsWith()
	}
	ranked := fmt.Sprintf(`
		ranked AS (
			SELECT a.id AS id, a.work_id AS work_id, a.format AS format,
			       COALESCE(a.current_size, a.original_size, 0) AS current_size,
			       w.title AS title, COALESCE(w.description, '') AS description,
			       COALESCE(w.publisher, '') AS publisher,
			       COALESCE(w.published_date, '') AS published_date,
			       COALESCE(w.language, '') AS language,
			       COALESCE(w.series, '') AS series, w.series_index AS series_index,
			       w.added_at AS added_at,
			       MAX(w.updated_at, a.updated_at) AS modified_at,
			       COALESCE((
				   SELECT group_concat(author_name, char(31))
				   FROM (
				       SELECT au.name AS author_name
				       FROM work_authors wa
				       JOIN authors au ON au.id = wa.author_id
				       WHERE wa.work_id = w.id
				       ORDER BY wa.author_order, au.name COLLATE NOCASE, au.id
				   )
			       ), '') AS authors,
			       ROW_NUMBER() OVER (
				   PARTITION BY w.id
				   ORDER BY (a.format = 'kepub') DESC, a.is_primary DESC, a.created_at, a.id
			       ) AS choice
			FROM %s
			WHERE %s
		)`, fromSQL, whereSQL)
	if withSQL != "" {
		withSQL += "," + ranked
	} else {
		withSQL = ranked
	}

	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		%s
		SELECT id, work_id, format, current_size, title, description,
		       publisher, published_date, language, series, series_index,
		       added_at, modified_at, authors
		FROM ranked
		WHERE choice = 1
		ORDER BY id
	`, withClause(withSQL)), args...)
	if err != nil {
		return nil, fmt.Errorf("list kobo candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]koboCandidate, 0, capacityHint)
	for rows.Next() {
		candidate, err := scanKoboCandidate(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kobo candidate rows: %w", err)
	}
	return candidates, nil
}

func scanKoboCandidate(row rowScanner) (koboCandidate, error) {
	var candidate koboCandidate
	var authors string
	err := row.Scan(
		&candidate.AssetID, &candidate.WorkID, &candidate.Format, &candidate.Size,
		&candidate.Title, &candidate.Description, &candidate.Publisher,
		&candidate.PublishedDate, &candidate.Language, &candidate.Series, &candidate.SeriesIndex,
		&candidate.AddedAt, &candidate.ModifiedAt, &authors,
	)
	if err != nil {
		return candidate, fmt.Errorf("scan kobo candidate: %w", err)
	}
	if authors != "" {
		candidate.Authors = strings.Split(authors, string(rune(31)))
	}
	fingerprintInput, err := json.Marshal(candidate.KoboPublication)
	if err != nil {
		return candidate, fmt.Errorf("encode kobo fingerprint: %w", err)
	}
	fingerprint := sha256.Sum256(fingerprintInput)
	candidate.Fingerprint = hex.EncodeToString(fingerprint[:])
	return candidate, nil
}

func listKoboChanges(ctx context.Context, tx *sql.Tx, connectionID string, after int64, limit int) ([]KoboChange, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ki.asset_id, ki.work_id, COALESCE(a.format, ''),
		       COALESCE(a.current_size, a.original_size, 0),
		       COALESCE(w.title, ''), COALESCE(w.description, ''),
		       COALESCE(w.publisher, ''), COALESCE(w.published_date, ''),
		       COALESCE(w.language, ''), COALESCE(w.series, ''), w.series_index,
		       COALESCE(w.added_at, ki.updated_at), COALESCE(MAX(w.updated_at, a.updated_at), ki.updated_at),
		       COALESCE((
			   SELECT group_concat(author_name, char(31))
			   FROM (
			       SELECT au.name AS author_name
			       FROM work_authors wa
			       JOIN authors au ON au.id = wa.author_id
			       WHERE wa.work_id = w.id
			       ORDER BY wa.author_order, au.name COLLATE NOCASE, au.id
			   )
		       ), ''),
		       ki.revision, ki.first_revision, ki.present, ki.updated_at
		FROM kobo_items ki
		LEFT JOIN assets a ON a.id = ki.asset_id
		LEFT JOIN works w ON w.id = ki.work_id
		WHERE ki.connection_id = ? AND ki.revision > ?
		ORDER BY ki.revision
		LIMIT ?
	`, connectionID, after, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list kobo changes: %w", err)
	}
	defer rows.Close()

	var changes []KoboChange
	for rows.Next() {
		var change KoboChange
		var authors string
		if err := rows.Scan(
			&change.AssetID, &change.WorkID, &change.Format, &change.Size,
			&change.Title, &change.Description, &change.Publisher,
			&change.PublishedDate, &change.Language, &change.Series, &change.SeriesIndex,
			&change.AddedAt, &change.ModifiedAt, &authors,
			&change.Revision, &change.FirstRevision, &change.Present, &change.ChangedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan kobo change: %w", err)
		}
		if authors != "" {
			change.Authors = strings.Split(authors, string(rune(31)))
		}
		changes = append(changes, change)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("kobo change rows: %w", err)
	}
	more := len(changes) > limit
	if more {
		changes = changes[:limit]
	}
	return changes, more, nil
}

// KoboPublicationForAsset verifies the last reconciled projection and live
// bytes. HTTP handlers separately enforce the owner's current visibility scope;
// shelf additions/removals become projection changes at the next library sync.
func (db *DB) KoboPublicationForAsset(connectionID, assetID string) (*KoboPublication, error) {
	row := db.QueryRow(`
		SELECT ki.asset_id, ki.work_id, a.format,
		       COALESCE(a.current_size, a.original_size, 0),
		       w.title, COALESCE(w.description, ''), COALESCE(w.publisher, ''),
		       COALESCE(w.published_date, ''),
		       COALESCE(w.language, ''), COALESCE(w.series, ''), w.series_index,
		       w.added_at, MAX(w.updated_at, a.updated_at),
		       COALESCE((
			   SELECT group_concat(author_name, char(31))
			   FROM (
			       SELECT au.name AS author_name
			       FROM work_authors wa
			       JOIN authors au ON au.id = wa.author_id
			       WHERE wa.work_id = w.id
			       ORDER BY wa.author_order, au.name COLLATE NOCASE, au.id
			   )
		       ), '')
		FROM kobo_items ki
		JOIN assets a ON a.id = ki.asset_id
		JOIN works w ON w.id = ki.work_id AND w.deleted_at IS NULL
		WHERE ki.connection_id = ? AND ki.asset_id = ? AND ki.present = 1
	`, connectionID, assetID)
	candidate, err := scanKoboCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKoboConnectionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &candidate.KoboPublication, nil
}
