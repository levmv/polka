package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrAssetNotFound      = errors.New("asset not found")
	ErrInvalidReaderInput = errors.New("invalid reader input")
)

const (
	ReaderFlowPaginated = "paginated"
	ReaderFlowScrolled  = "scrolled"

	ReaderStyleOriginal = "original"
	ReaderStylePaper    = "paper"
	ReaderStyleCustom   = "custom"

	DefaultReaderFontScale         = 0
	DefaultReaderCustomColumnWidth = 760
	DefaultReaderCustomLineHeight  = 1.72
)

type ReaderState struct {
	UserID     string
	AssetID    string
	WorkID     string
	Progress   float64
	Locator    ReaderLocator
	LastReadAt int64
	UpdatedAt  int64
}

type ReaderPreferences struct {
	UserID            string
	EPUBFlow          string
	DisplayStyle      string
	FontScale         int
	CustomColumnWidth int
	CustomLineHeight  float64
	UpdatedAt         int64
}

type ContinueReadingRow struct {
	BookSummaryRow
	AssetID    string
	Progress   float64
	LastReadAt int64
}

func (db *DB) GetReaderState(userID, assetID string) (*ReaderState, error) {
	return getReaderState(db, userID, assetID)
}

func getReaderState(queryer Queryer, userID, assetID string) (*ReaderState, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	state := &ReaderState{UserID: userID, AssetID: assetID, Locator: EmptyReaderLocator()}
	var locator string
	err := queryer.QueryRow(`
		SELECT a.work_id,
		       COALESCE(s.progress, 0),
		       COALESCE(s.locator, '{}'),
		       COALESCE(s.last_read_at, 0),
		       COALESCE(s.updated_at, 0)
		FROM assets a
		LEFT JOIN user_asset_state s ON s.asset_id = a.id AND s.user_id = ?
		WHERE a.id = ?
	`, userID, assetID).Scan(&state.WorkID, &state.Progress, &locator, &state.LastReadAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get reader state: %w", err)
	}
	normalized, err := ReaderLocatorFromString(locator)
	if err != nil {
		return nil, fmt.Errorf("get reader state locator: %w", err)
	}
	state.Locator = normalized
	return state, nil
}

func (db *DB) TouchReaderStateAndAdvanceStatus(
	ctx context.Context,
	userID, assetID string,
	source ReadingStatusSource,
) (*ReaderState, ReadingStatusChange, error) {
	var state *ReaderState
	var change ReadingStatusChange
	err := db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = touchReaderState(tx, userID, assetID)
		if err != nil {
			return err
		}
		// Opening is an unread -> reading signal. Deliberately use zero rather
		// than the stored percentage: reopening an old last-page position after
		// "Read again" must not immediately finish the book.
		change, err = advanceReadingStatus(tx, userID, state.WorkID, 0, source)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return state, change, nil
}

func touchReaderState(tx *sql.Tx, userID, assetID string) (*ReaderState, error) {
	if _, err := getReaderState(tx, userID, assetID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_asset_state (user_id, asset_id, last_read_at, updated_at)
		VALUES (?, ?, unixepoch(), unixepoch())
		ON CONFLICT(user_id, asset_id) DO UPDATE SET
			last_read_at = unixepoch(),
			updated_at = unixepoch()
	`, userID, assetID); err != nil {
		return nil, fmt.Errorf("touch reader state: %w", err)
	}
	return getReaderState(tx, userID, assetID)
}

func (db *DB) SaveReaderStateAndAdvanceStatus(
	ctx context.Context,
	userID, assetID string,
	progress float64,
	locator ReaderLocator,
	source ReadingStatusSource,
) (*ReaderState, ReadingStatusChange, error) {
	normalized, err := validateReaderPosition(progress, locator)
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	var state *ReaderState
	var change ReadingStatusChange
	err = db.Transact(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = saveReaderState(tx, userID, assetID, progress, normalized)
		if err != nil {
			return err
		}
		change, err = advanceReadingStatus(tx, userID, state.WorkID, progress, source)
		return err
	})
	if err != nil {
		return nil, ReadingStatusChange{}, err
	}
	return state, change, nil
}

func validateReaderPosition(progress float64, locator ReaderLocator) (ReaderLocator, error) {
	if progress < 0 || progress > 1 {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader progress must be between 0 and 1")
	}
	normalized, err := NewReaderLocator(locator)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func saveReaderState(tx *sql.Tx, userID, assetID string, progress float64, locator ReaderLocator) (*ReaderState, error) {
	if _, err := getReaderState(tx, userID, assetID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO user_asset_state (user_id, asset_id, progress, locator, last_read_at, updated_at)
		VALUES (?, ?, ?, ?, unixepoch(), unixepoch())
		ON CONFLICT(user_id, asset_id) DO UPDATE SET
			progress = excluded.progress,
			locator = excluded.locator,
			last_read_at = unixepoch(),
			updated_at = unixepoch()
	`, userID, assetID, progress, locator.String()); err != nil {
		return nil, fmt.Errorf("save reader state: %w", err)
	}
	return getReaderState(tx, userID, assetID)
}

func (db *DB) ResetReaderState(userID, assetID string) error {
	if _, err := db.GetReaderState(userID, assetID); err != nil {
		return err
	}
	if _, err := db.Exec(`
		DELETE FROM user_asset_state
		WHERE user_id = ? AND asset_id = ?
	`, userID, assetID); err != nil {
		return fmt.Errorf("reset reader state: %w", err)
	}
	return nil
}

func (db *DB) GetReaderPreferences(userID string) (*ReaderPreferences, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	prefs := defaultReaderPreferences(userID)
	err := db.QueryRow(`
			SELECT epub_flow, display_style, font_scale, custom_column_width, custom_line_height, updated_at
			FROM user_reader_preferences
			WHERE user_id = ?
		`, userID).Scan(&prefs.EPUBFlow, &prefs.DisplayStyle, &prefs.FontScale, &prefs.CustomColumnWidth, &prefs.CustomLineHeight, &prefs.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return prefs, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reader preferences: %w", err)
	}
	return prefs, nil
}

func (db *DB) SaveReaderPreferences(userID string, prefs ReaderPreferences) (*ReaderPreferences, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	prefs = normalizeReaderPreferences(userID, prefs)
	if !validReaderFlow(prefs.EPUBFlow) {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader epub flow must be paginated or scrolled")
	}
	if !validReaderStyle(prefs.DisplayStyle) {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader display style must be original, paper, or custom")
	}
	if prefs.FontScale < -4 || prefs.FontScale > 6 {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader font scale must be between -4 and 6")
	}
	if prefs.CustomColumnWidth < 560 || prefs.CustomColumnWidth > 920 {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader custom column width must be between 560 and 920")
	}
	if prefs.CustomLineHeight < 1.2 || prefs.CustomLineHeight > 2.2 {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader custom line height must be between 1.2 and 2.2")
	}
	if _, err := db.Exec(`
			INSERT INTO user_reader_preferences
				(user_id, epub_flow, display_style, font_scale, custom_column_width, custom_line_height, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, unixepoch())
			ON CONFLICT(user_id) DO UPDATE SET
				epub_flow = excluded.epub_flow,
				display_style = excluded.display_style,
				font_scale = excluded.font_scale,
				custom_column_width = excluded.custom_column_width,
				custom_line_height = excluded.custom_line_height,
				updated_at = unixepoch()
		`, userID, prefs.EPUBFlow, prefs.DisplayStyle, prefs.FontScale, prefs.CustomColumnWidth, prefs.CustomLineHeight); err != nil {
		return nil, fmt.Errorf("save reader preferences: %w", err)
	}
	return db.GetReaderPreferences(userID)
}

func ListContinueReading(queryer Queryer, scope VisibilityScope, userID string, limit int) ([]ContinueReadingRow, error) {
	if userID == "" {
		return nil, ErrUserIDRequired
	}
	if limit <= 0 {
		limit = 8
	}

	where, args := scope.AppendWorkWhere(`s.user_id = ?
				AND s.last_read_at > 0
				AND s.progress < 0.995
				AND rs.status = 'reading'
				AND w.deleted_at IS NULL`, "w.id", userID)
	args = append(args, limit)

	queryStr := fmt.Sprintf(`
		WITH latest AS (
			SELECT
				a.work_id,
				s.asset_id,
				s.progress,
				s.last_read_at,
				ROW_NUMBER() OVER (
					PARTITION BY a.work_id
					ORDER BY s.last_read_at DESC, s.updated_at DESC, s.asset_id ASC
				) AS rn
			FROM user_asset_state s
			JOIN assets a ON a.id = s.asset_id
			JOIN works w ON w.id = a.work_id
			JOIN user_work_reading_state rs
				ON rs.user_id = s.user_id AND rs.work_id = a.work_id
			WHERE `+where+`
		)
		SELECT %s,
			latest.asset_id,
			latest.progress,
			latest.last_read_at
		FROM latest
		JOIN works w ON w.id = latest.work_id
		WHERE latest.rn = 1
		ORDER BY latest.last_read_at DESC
		LIMIT ?
	`, bookSummaryColumns)

	rows, err := queryer.Query(queryStr, args...)
	if err != nil {
		return nil, fmt.Errorf("list continue reading query: %w", err)
	}
	defer rows.Close()

	var out []ContinueReadingRow
	for rows.Next() {
		var r ContinueReadingRow
		if err := rows.Scan(
			&r.ID,
			&r.Title,
			&r.Series,
			&r.SeriesIndex,
			&r.Tags,
			&r.CoverVersion,
			&r.Date,
			&r.AssetID,
			&r.Progress,
			&r.LastReadAt,
		); err != nil {
			return nil, fmt.Errorf("list continue reading scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list continue reading rows: %w", err)
	}
	return out, nil
}

func validReaderFlow(flow string) bool {
	return flow == ReaderFlowPaginated || flow == ReaderFlowScrolled
}

func validReaderStyle(style string) bool {
	return style == ReaderStyleOriginal || style == ReaderStylePaper || style == ReaderStyleCustom
}

func defaultReaderPreferences(userID string) *ReaderPreferences {
	return &ReaderPreferences{
		UserID:            userID,
		EPUBFlow:          ReaderFlowPaginated,
		DisplayStyle:      ReaderStylePaper,
		FontScale:         DefaultReaderFontScale,
		CustomColumnWidth: DefaultReaderCustomColumnWidth,
		CustomLineHeight:  DefaultReaderCustomLineHeight,
	}
}

func normalizeReaderPreferences(userID string, prefs ReaderPreferences) ReaderPreferences {
	defaults := defaultReaderPreferences(userID)
	defaults.EPUBFlow = prefs.EPUBFlow
	defaults.DisplayStyle = prefs.DisplayStyle
	defaults.FontScale = prefs.FontScale
	defaults.CustomColumnWidth = prefs.CustomColumnWidth
	defaults.CustomLineHeight = prefs.CustomLineHeight
	if defaults.EPUBFlow == "" {
		defaults.EPUBFlow = ReaderFlowPaginated
	}
	if defaults.DisplayStyle == "" {
		defaults.DisplayStyle = ReaderStylePaper
	}
	if defaults.CustomColumnWidth == 0 {
		defaults.CustomColumnWidth = DefaultReaderCustomColumnWidth
	}
	if defaults.CustomLineHeight == 0 {
		defaults.CustomLineHeight = DefaultReaderCustomLineHeight
	}
	return *defaults
}
