package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const KoboSyncPageLimit = 100

var (
	ErrKoboConnectionNotFound = errors.New("kobo connection not found")
	ErrKoboConnectionExists   = errors.New("kobo connection already exists")
	ErrKoboInvalidCursor      = errors.New("invalid kobo sync cursor")
)

type KoboConnection struct {
	ID         int64
	UserID     int64
	ShelfID    sql.NullInt64
	ShelfName  string
	Token      string
	Revision   int64
	CreatedAt  int64
	UpdatedAt  int64
	LastUsedAt sql.NullInt64
}

type KoboAsset struct {
	AssetID int64
	BookID  int64
	Format  string
	AddedAt int64
}

// KoboPublication is the metadata needed by the native Kobo adapter.
type KoboPublication struct {
	KoboAsset
	Size          int64
	Title         string
	Description   string
	Publisher     string
	PublishedDate string
	Language      string
	Series        string
	SeriesIndex   sql.NullFloat64
	Authors       []string
	ModifiedAt    int64
	CoverVersion  int
}

type KoboChange struct {
	KoboPublication
	Revision      int64
	FirstRevision int64 // Start of the current appearance; re-adding a book starts a new one.
	Present       bool
	ChangedAt     int64
}

type koboCandidate struct {
	AssetID     int64
	BookID      int64
	Fingerprint []byte
}

// CreateKoboConnection creates a retrievable URL credential for a selected shelf.
func (db *DB) CreateKoboConnection(ctx context.Context, userID, shelfID int64) (*KoboConnection, error) {
	token := newDeviceToken()
	var connection *KoboConnection
	err := db.Transact(ctx, func(tx *Tx) error {
		shelf, err := GetShelfForUser(tx, shelfID, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO kobo_connections (user_id, shelf_id, token)
			VALUES (?, ?, ?)
		`, userID, shelf.ID, token); err != nil {
			if isUniqueViolation(err) {
				return ErrKoboConnectionExists
			}
			return fmt.Errorf("insert kobo connection: %w", err)
		}
		connection, err = KoboConnectionForUser(tx, userID)
		return err
	})
	return connection, err
}

// SetKoboConnectionShelf preserves the credential and revision history. The next
// sync reconciles the new shelf against the previous one, including removals.
func (db *DB) SetKoboConnectionShelf(ctx context.Context, userID, shelfID int64) (*KoboConnection, error) {
	var connection *KoboConnection
	err := db.Transact(ctx, func(tx *Tx) error {
		if _, err := GetShelfForUser(tx, shelfID, userID); err != nil {
			return err
		}
		result, err := tx.Exec(`
			UPDATE kobo_connections SET shelf_id = ?, updated_at = unixepoch()
			WHERE user_id = ?
		`, shelfID, userID)
		if err != nil {
			return fmt.Errorf("change kobo shelf: %w", err)
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrKoboConnectionNotFound
		}
		connection, err = KoboConnectionForUser(tx, userID)
		return err
	})
	return connection, err
}

func KoboConnectionForUser(queryer Queryer, userID int64) (*KoboConnection, error) {
	return scanKoboConnection(queryer.QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, COALESCE(s.name, ''), kc.token, kc.revision,
		       kc.created_at, kc.updated_at, kc.last_used_at
		FROM kobo_connections kc
		LEFT JOIN shelves s ON s.id = kc.shelf_id
		WHERE kc.user_id = ?
	`, userID))
}

func scanKoboConnection(row *sql.Row) (*KoboConnection, error) {
	var connection KoboConnection
	err := row.Scan(
		&connection.ID, &connection.UserID, &connection.ShelfID, &connection.ShelfName, &connection.Token,
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

func (db *DB) DeleteKoboConnection(ctx context.Context, userID int64) error {
	result, err := db.Write(ctx).Exec("DELETE FROM kobo_connections WHERE user_id = ?", userID)
	if err != nil {
		return fmt.Errorf("delete kobo connection: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrKoboConnectionNotFound
	}
	return nil
}

// KoboConnectionByToken authenticates a Kobo URL using its device credential.
// The last-used timestamp is throttled so cover and download traffic does not
// turn every request into a database write.
func (db *DB) KoboConnectionByToken(ctx context.Context, token string) (*KoboConnection, bool, error) {
	token = normalizeDeviceToken(token)
	if token == "" {
		return nil, false, nil
	}
	connection, err := scanKoboConnection(db.Read(ctx).QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, COALESCE(s.name, ''), kc.token, kc.revision,
		       kc.created_at, kc.updated_at, kc.last_used_at
		FROM kobo_connections kc
		LEFT JOIN shelves s ON s.id = kc.shelf_id
		WHERE kc.token = ?
	`, token))
	if errors.Is(err, ErrKoboConnectionNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	now := time.Now().Unix()
	if !connection.LastUsedAt.Valid || now-connection.LastUsedAt.Int64 >= 3600 {
		updated, err := db.ExecBestEffort(ctx, `
			UPDATE kobo_connections SET last_used_at = ? WHERE id = ?
		`, now, connection.ID)
		if err != nil {
			return nil, false, fmt.Errorf("bump kobo connection: %w", err)
		}
		if updated {
			connection.LastUsedAt = sql.NullInt64{Int64: now, Valid: true}
		}
	}
	return connection, true, nil
}

// SyncKoboConnection reconciles the selected shelf and reads one stable revision
// page in the same transaction. A retry with the same cursor therefore returns
// the same logical page unless real source data changed in between.
func (db *DB) SyncKoboConnection(ctx context.Context, connectionID, after int64, limit int) ([]KoboChange, int64, bool, error) {
	if after < 0 || limit < 1 || limit > KoboSyncPageLimit {
		return nil, 0, false, ErrKoboInvalidCursor
	}

	var changes []KoboChange
	var currentRevision int64
	var more bool
	err := db.Transact(ctx, func(tx *Tx) error {
		connection, shelf, scope, err := loadKoboSyncState(tx, connectionID)
		if err != nil {
			return err
		}
		currentRevision, err = reconcileKoboItems(tx, connection, shelf, scope)
		if err != nil {
			return err
		}
		if after > currentRevision {
			return ErrKoboInvalidCursor
		}
		changes, more, err = listKoboChanges(tx, connectionID, after, limit)
		return err
	})
	if err != nil {
		return nil, 0, false, err
	}
	return changes, currentRevision, more, nil
}

func loadKoboSyncState(tx *Tx, connectionID int64) (*KoboConnection, *Shelf, VisibilityScope, error) {
	var connection KoboConnection
	var role, contentScope string
	err := tx.QueryRow(`
		SELECT kc.id, kc.user_id, kc.shelf_id, kc.revision,
		       u.role, u.content_scope
		FROM kobo_connections kc
		JOIN users u ON u.id = kc.user_id
		WHERE kc.id = ?
	`, connectionID).Scan(
		&connection.ID, &connection.UserID, &connection.ShelfID, &connection.Revision,
		&role, &contentScope,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, VisibilityScope{}, ErrKoboConnectionNotFound
	}
	if err != nil {
		return nil, nil, VisibilityScope{}, fmt.Errorf("get kobo sync context: %w", err)
	}
	scope := FullVisibilityScope()
	if role == RoleReader && contentScope == ContentScopeShelves {
		scope = VisibilityScope{UserID: connection.UserID, ContentScope: ContentScopeShelves}
	}
	var shelf *Shelf
	if connection.ShelfID.Valid {
		shelf, err = GetShelfForUser(tx, connection.ShelfID.Int64, connection.UserID)
		// An inaccessible shelf is empty; other read errors must abort sync.
		if err != nil && !errors.Is(err, ErrShelfNotFound) {
			return nil, nil, VisibilityScope{}, err
		}
	}
	return &connection, shelf, scope, nil
}

func reconcileKoboItems(tx *Tx, connection *KoboConnection, shelf *Shelf, scope VisibilityScope) (int64, error) {
	type existingItem struct {
		Fingerprint []byte
		Present     bool
	}
	existing := make(map[int64]existingItem)
	rows, err := tx.Query(`
		SELECT asset_id, fingerprint, present
		FROM kobo_items
		WHERE connection_id = ?
	`, connection.ID)
	if err != nil {
		return 0, fmt.Errorf("list current kobo items: %w", err)
	}
	for rows.Next() {
		var assetID int64
		var item existingItem
		if err := rows.Scan(&assetID, &item.Fingerprint, &item.Present); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan current kobo item: %w", err)
		}
		existing[assetID] = item
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close current kobo items: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate current kobo items: %w", err)
	}

	candidates, err := listKoboCandidates(tx, connection.UserID, shelf, scope, len(existing))
	if err != nil {
		return 0, err
	}

	revision := connection.Revision
	for _, candidate := range candidates {
		old, found := existing[candidate.AssetID]
		// Rows left after processing all candidates are removals or tombstones.
		delete(existing, candidate.AssetID)
		if found && old.Present && bytes.Equal(old.Fingerprint, candidate.Fingerprint) {
			continue
		}
		revision++
		if !found {
			if _, err := tx.Exec(`
				INSERT INTO kobo_items
				    (connection_id, asset_id, book_id, fingerprint, present, revision, first_revision)
				VALUES (?, ?, ?, ?, 1, ?, ?)
			`, connection.ID, candidate.AssetID, candidate.BookID, candidate.Fingerprint, revision, revision); err != nil {
				return 0, fmt.Errorf("insert kobo item: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(`
			UPDATE kobo_items
			SET book_id = ?, fingerprint = ?, present = 1, revision = ?,
			    first_revision = CASE WHEN present = 0 THEN ? ELSE first_revision END,
			    updated_at = unixepoch()
			WHERE connection_id = ? AND asset_id = ?
		`, candidate.BookID, candidate.Fingerprint, revision, revision, connection.ID, candidate.AssetID); err != nil {
			return 0, fmt.Errorf("update kobo item: %w", err)
		}
	}

	var removals []int64
	for assetID, item := range existing {
		if item.Present {
			removals = append(removals, assetID)
		}
	}
	slices.Sort(removals)
	for _, assetID := range removals {
		revision++
		if _, err := tx.Exec(`
			UPDATE kobo_items
			SET present = 0, revision = ?, updated_at = unixepoch()
			WHERE connection_id = ? AND asset_id = ?
		`, revision, connection.ID, assetID); err != nil {
			return 0, fmt.Errorf("tombstone kobo item: %w", err)
		}
	}

	if revision != connection.Revision {
		if _, err := tx.Exec(`
			UPDATE kobo_connections SET revision = ?, updated_at = unixepoch() WHERE id = ?
		`, revision, connection.ID); err != nil {
			return 0, fmt.Errorf("advance kobo revision: %w", err)
		}
	}
	return revision, nil
}

func listKoboCandidates(tx *Tx, userID int64, shelf *Shelf, scope VisibilityScope, capacityHint int) ([]koboCandidate, error) {
	if shelf == nil {
		return nil, nil
	}
	var withSQL, fromSQL, whereSQL string
	var args []any

	if shelf.Kind == ShelfManual {
		joined := "shelf_books sb JOIN books b ON b.id = sb.book_id JOIN assets a ON a.book_id = b.id"
		withSQL, fromSQL, args = scope.joinVisibleBooks(joined)
		whereSQL = "b.deleted_at IS NULL AND a.format IN ('kepub', 'epub') AND sb.shelf_id = ?"
		args = append(args, shelf.ID)
	} else {
		plan := newBookSearchPlan(scope, userID, shelf.Query)
		if !plan.hasClauses {
			return nil, nil
		}
		withSQL = plan.withSQL
		fromSQL = plan.fromSQL + " JOIN assets a ON a.book_id = b.id"
		whereSQL = plan.whereSQL + " AND a.format IN ('kepub', 'epub')"
		args = plan.argsWith()
	}
	ranked := fmt.Sprintf(`
		ranked AS (
			SELECT a.id AS id, a.book_id AS book_id, a.format AS format,
			       COALESCE(a.current_size, a.original_size, 0) AS current_size,
			       b.title AS title, COALESCE(b.description, '') AS description,
			       COALESCE(b.publisher, '') AS publisher,
			       COALESCE(b.published_date, '') AS published_date,
			       COALESCE(b.language, '') AS language,
			       COALESCE(b.series, '') AS series, b.series_index AS series_index,
			       b.added_at AS added_at,
			       MAX(b.updated_at, a.updated_at) AS modified_at, b.cover_version,
			       COALESCE((
				   SELECT group_concat(author_name, char(31))
				   FROM (
				       SELECT au.name AS author_name
				       FROM book_authors ba
				       JOIN authors au ON au.id = ba.author_id
				       WHERE ba.book_id = b.id
				       ORDER BY ba.author_order, au.name COLLATE NOCASE, au.id
				   )
			       ), '') AS authors,
			       ROW_NUMBER() OVER (
				   PARTITION BY b.id
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

	rows, err := tx.Query(fmt.Sprintf(`
		%s
		SELECT ranked.id, ranked.book_id, format, current_size, title, description,
		       publisher, published_date, language, series, series_index,
		       added_at, ranked.modified_at, cover_version, authors,
		       COALESCE(p.revision, 0), COALESCE(rs.last_event_id, 0)
		FROM ranked
		LEFT JOIN reading_positions p ON p.asset_id = ranked.id AND p.user_id = ?
		LEFT JOIN user_book_reading_state rs ON rs.book_id = ranked.book_id AND rs.user_id = ?
		WHERE choice = 1
		ORDER BY id
	`, withClause(withSQL)), append(args, userID, userID)...)
	if err != nil {
		return nil, fmt.Errorf("list kobo candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]koboCandidate, 0, capacityHint)
	for rows.Next() {
		// Coordinate conversion leaves these source versions unchanged.
		var snapshot struct {
			KoboPublication
			PositionRevision int64
			StatusEventID    int64
		}
		snapshot.KoboPublication, err = scanKoboPublication(rows, &snapshot.PositionRevision, &snapshot.StatusEventID)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return nil, fmt.Errorf("encode kobo fingerprint: %w", err)
		}
		fingerprint := sha256.Sum256(encoded)
		// Keep 128 bits for equality checks in the Kobo change feed.
		candidates = append(candidates, koboCandidate{
			AssetID: snapshot.AssetID, BookID: snapshot.BookID, Fingerprint: fingerprint[:16],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kobo candidate rows: %w", err)
	}
	return candidates, nil
}

func scanKoboPublication(row rowScanner, extra ...any) (KoboPublication, error) {
	var publication KoboPublication
	var authors string
	dest := []any{&publication.AssetID, &publication.BookID, &publication.Format, &publication.Size,
		&publication.Title, &publication.Description, &publication.Publisher,
		&publication.PublishedDate, &publication.Language, &publication.Series, &publication.SeriesIndex,
		&publication.AddedAt, &publication.ModifiedAt, &publication.CoverVersion, &authors}
	err := row.Scan(append(dest, extra...)...)
	if err != nil {
		return publication, fmt.Errorf("scan kobo publication: %w", err)
	}
	if authors != "" {
		publication.Authors = strings.Split(authors, string(rune(31)))
	}
	return publication, nil
}

func listKoboChanges(tx *Tx, connectionID, after int64, limit int) ([]KoboChange, bool, error) {
	rows, err := tx.Query(`
		SELECT ki.asset_id, ki.book_id, COALESCE(a.format, ''),
		       COALESCE(a.current_size, a.original_size, 0),
		       COALESCE(b.title, ''), COALESCE(b.description, ''),
		       COALESCE(b.publisher, ''), COALESCE(b.published_date, ''),
		       COALESCE(b.language, ''), COALESCE(b.series, ''), b.series_index,
		       COALESCE(b.added_at, ki.updated_at), COALESCE(MAX(b.updated_at, a.updated_at), ki.updated_at),
		       COALESCE(b.cover_version, 0),
		       COALESCE((
			   SELECT group_concat(author_name, char(31))
			   FROM (
			       SELECT au.name AS author_name
			       FROM book_authors ba
			       JOIN authors au ON au.id = ba.author_id
			       WHERE ba.book_id = b.id
			       ORDER BY ba.author_order, au.name COLLATE NOCASE, au.id
			   )
		       ), ''),
		       ki.revision, ki.first_revision, ki.present, ki.updated_at
		FROM kobo_items ki
		LEFT JOIN assets a ON a.id = ki.asset_id
		LEFT JOIN books b ON b.id = ki.book_id
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
		change.KoboPublication, err = scanKoboPublication(rows,
			&change.Revision, &change.FirstRevision, &change.Present, &change.ChangedAt,
		)
		if err != nil {
			return nil, false, err
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

// KoboAssetForConnection checks the last reconciled projection and current
// visibility. Shelf additions/removals enter the projection at library sync.
func KoboAssetForConnection(queryer Queryer, scope VisibilityScope, connectionID, assetID int64) (*KoboAsset, error) {
	where, args := scope.AppendBookWhere(
		"ki.connection_id = ? AND ki.asset_id = ? AND ki.present = 1 AND b.deleted_at IS NULL",
		"b.id", connectionID, assetID,
	)
	var asset KoboAsset
	err := queryer.QueryRow(`
		SELECT a.id, a.book_id, a.format, b.added_at
		FROM kobo_items ki
		JOIN assets a ON a.id = ki.asset_id
		JOIN books b ON b.id = a.book_id
		WHERE `+where, args...).Scan(&asset.AssetID, &asset.BookID, &asset.Format, &asset.AddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get kobo asset: %w", err)
	}
	return &asset, nil
}

// KoboPublicationForAsset loads metadata after the caller checks access.
func KoboPublicationForAsset(queryer Queryer, assetID int64) (*KoboPublication, error) {
	row := queryer.QueryRow(`
		SELECT a.id, a.book_id, a.format,
		       COALESCE(a.current_size, a.original_size, 0),
		       b.title, COALESCE(b.description, ''), COALESCE(b.publisher, ''),
		       COALESCE(b.published_date, ''),
		       COALESCE(b.language, ''), COALESCE(b.series, ''), b.series_index,
		       b.added_at, MAX(b.updated_at, a.updated_at), b.cover_version,
		       COALESCE((
			   SELECT group_concat(author_name, char(31))
			   FROM (
			       SELECT au.name AS author_name
			       FROM book_authors ba
			       JOIN authors au ON au.id = ba.author_id
			       WHERE ba.book_id = b.id
			       ORDER BY ba.author_order, au.name COLLATE NOCASE, au.id
			   )
		       ), '')
		FROM assets a
		JOIN books b ON b.id = a.book_id AND b.deleted_at IS NULL
		WHERE a.id = ?
	`, assetID)
	publication, err := scanKoboPublication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, err
	}
	return &publication, nil
}
