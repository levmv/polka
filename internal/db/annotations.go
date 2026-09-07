package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrAnnotationNotFound = errors.New("annotation not found")
	ErrInvalidAnnotation  = errors.New("invalid annotation")
)

const (
	AnnotationKindHighlight = "highlight"
	AnnotationColorYellow   = "yellow"

	MaxAnnotationCFILength     = 2048
	MaxAnnotationQuoteLength   = 1200
	MaxAnnotationContextLength = 500
	MaxAnnotationNoteLength    = 4000
)

const annotationColumns = `id, user_id, asset_id, kind, cfi, quote,
	context_before, context_after, note, color, created_at, updated_at`

type Annotation struct {
	ID            int64
	UserID        int64
	AssetID       int64
	Kind          string
	CFI           string
	Quote         string
	ContextBefore string
	ContextAfter  string
	Note          string
	Color         string
	CreatedAt     int64
	UpdatedAt     int64
}

type AnnotationCreate struct {
	Kind          string
	CFI           string
	Quote         string
	ContextBefore string
	ContextAfter  string
	Note          string
	Color         string
}

type AnnotationNoteUpdate struct {
	Note string
}

func ListAnnotations(queryer Queryer, userID, assetID int64) ([]Annotation, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if _, err := GetReaderState(queryer, userID, assetID); err != nil {
		return nil, err
	}
	rows, err := queryer.Query(`
		SELECT `+annotationColumns+`
		FROM user_annotations
		WHERE user_id = ? AND asset_id = ?
		ORDER BY created_at ASC, id ASC
	`, userID, assetID)
	if err != nil {
		return nil, fmt.Errorf("list annotations: %w", err)
	}
	defer rows.Close()

	var out []Annotation
	for rows.Next() {
		var ann Annotation
		if err := scanAnnotation(rows, &ann); err != nil {
			return nil, err
		}
		out = append(out, ann)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list annotations rows: %w", err)
	}
	return out, nil
}

func (db *DB) CreateAnnotation(ctx context.Context, userID, assetID int64, input AnnotationCreate) (Annotation, error) {
	if userID <= 0 {
		return Annotation{}, ErrUserIDRequired
	}
	if _, err := GetReaderState(db.Read(ctx), userID, assetID); err != nil {
		return Annotation{}, err
	}
	ann, err := normalizeAnnotation(userID, assetID, input)
	if err != nil {
		return Annotation{}, err
	}
	err = db.Transact(ctx, func(tx *Tx) error {
		return scanAnnotation(tx.QueryRow(`
			INSERT INTO user_annotations
				(user_id, asset_id, kind, cfi, quote, context_before, context_after, note, color)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, asset_id, kind, cfi) DO UPDATE SET
				quote = excluded.quote,
				context_before = excluded.context_before,
				context_after = excluded.context_after,
				note = CASE
					WHEN excluded.note = '' THEN user_annotations.note
					ELSE excluded.note
				END,
				color = excluded.color,
				updated_at = unixepoch()
			RETURNING `+annotationColumns,
			ann.UserID, ann.AssetID, ann.Kind, ann.CFI, ann.Quote, ann.ContextBefore, ann.ContextAfter, ann.Note, ann.Color), &ann)
	})
	if err != nil {
		return Annotation{}, fmt.Errorf("create annotation: %w", err)
	}
	return ann, nil
}

func (db *DB) UpdateAnnotationNote(ctx context.Context, userID, assetID, annotationID int64, input AnnotationNoteUpdate) (Annotation, error) {
	if userID <= 0 {
		return Annotation{}, ErrUserIDRequired
	}
	note, err := normalizeAnnotationNote(input.Note)
	if err != nil {
		return Annotation{}, err
	}
	if _, err := db.Write(ctx).Exec(`
		UPDATE user_annotations
		SET note = ?, updated_at = unixepoch()
		WHERE id = ? AND user_id = ? AND asset_id = ?
	`, note, annotationID, userID, assetID); err != nil {
		return Annotation{}, fmt.Errorf("update annotation note: %w", err)
	}
	return GetAnnotationByID(db.Read(ctx), userID, assetID, annotationID)
}

func GetAnnotationByID(queryer Queryer, userID, assetID, annotationID int64) (Annotation, error) {
	var ann Annotation
	err := scanAnnotation(queryer.QueryRow(`
		SELECT `+annotationColumns+`
		FROM user_annotations
		WHERE id = ? AND user_id = ? AND asset_id = ?
	`, annotationID, userID, assetID), &ann)
	if errors.Is(err, sql.ErrNoRows) {
		return Annotation{}, ErrAnnotationNotFound
	}
	if err != nil {
		return Annotation{}, fmt.Errorf("get annotation: %w", err)
	}
	return ann, nil
}

func (db *DB) DeleteAnnotation(ctx context.Context, userID, assetID, annotationID int64) error {
	if userID <= 0 {
		return ErrUserIDRequired
	}
	res, err := db.Write(ctx).Exec(`
		DELETE FROM user_annotations
		WHERE id = ? AND user_id = ? AND asset_id = ?
	`, annotationID, userID, assetID)
	if err != nil {
		return fmt.Errorf("delete annotation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete annotation rows: %w", err)
	}
	if n == 0 {
		return ErrAnnotationNotFound
	}
	return nil
}

func scanAnnotation(scanner rowScanner, ann *Annotation) error {
	if err := scanner.Scan(
		&ann.ID,
		&ann.UserID,
		&ann.AssetID,
		&ann.Kind,
		&ann.CFI,
		&ann.Quote,
		&ann.ContextBefore,
		&ann.ContextAfter,
		&ann.Note,
		&ann.Color,
		&ann.CreatedAt,
		&ann.UpdatedAt,
	); err != nil {
		return fmt.Errorf("scan annotation: %w", err)
	}
	return nil
}

func normalizeAnnotation(userID, assetID int64, input AnnotationCreate) (Annotation, error) {
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		kind = AnnotationKindHighlight
	}
	color := strings.TrimSpace(input.Color)
	if color == "" {
		color = AnnotationColorYellow
	}
	ann := Annotation{
		UserID:        userID,
		AssetID:       assetID,
		Kind:          kind,
		CFI:           strings.TrimSpace(input.CFI),
		Quote:         strings.TrimSpace(input.Quote),
		ContextBefore: strings.TrimSpace(input.ContextBefore),
		ContextAfter:  strings.TrimSpace(input.ContextAfter),
		Color:         color,
	}
	note, err := normalizeAnnotationNote(input.Note)
	if err != nil {
		return Annotation{}, err
	}
	ann.Note = note
	if ann.Kind != AnnotationKindHighlight {
		return Annotation{}, ErrInvalidAnnotation
	}
	if ann.Color != AnnotationColorYellow {
		return Annotation{}, ErrInvalidAnnotation
	}
	if ann.CFI == "" || ann.Quote == "" {
		return Annotation{}, ErrInvalidAnnotation
	}
	if utf8.RuneCountInString(ann.CFI) > MaxAnnotationCFILength ||
		utf8.RuneCountInString(ann.Quote) > MaxAnnotationQuoteLength ||
		utf8.RuneCountInString(ann.ContextBefore) > MaxAnnotationContextLength ||
		utf8.RuneCountInString(ann.ContextAfter) > MaxAnnotationContextLength {
		return Annotation{}, ErrInvalidAnnotation
	}
	return ann, nil
}

func normalizeAnnotationNote(note string) (string, error) {
	normalized := strings.TrimSpace(note)
	if utf8.RuneCountInString(normalized) > MaxAnnotationNoteLength {
		return "", ErrInvalidAnnotation
	}
	return normalized, nil
}
