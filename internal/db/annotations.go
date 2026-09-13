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
	AnnotationColorYellow      = "yellow"
	MaxAnnotationQuoteLength   = 32768
	MaxAnnotationContextLength = 500
	MaxAnnotationNoteLength    = 4000
)

const annotationColumns = `id, user_id, asset_id, locator,
    quote, context_before, context_after, note, color, revision, deleted, created_at, updated_at`

type Annotation struct {
	ID            int64
	UserID        int64
	AssetID       int64
	Locator       Locator
	Quote         string
	ContextBefore string
	ContextAfter  string
	Note          string
	Color         string
	Revision      int64
	Deleted       bool
	CreatedAt     int64
	UpdatedAt     int64
}

type AnnotationCreate struct {
	Locator       Locator
	Quote         string
	ContextBefore string
	ContextAfter  string
	Note          string
	Color         string
}

type AnnotationUpdate struct {
	Revision int64
	Note     *string
	Color    *string
}

func ListAnnotations(queryer Queryer, userID, assetID int64) ([]Annotation, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if err := requireAsset(queryer, assetID); err != nil {
		return nil, err
	}
	return listAnnotations(queryer, `SELECT `+annotationColumns+` FROM user_annotations
        WHERE user_id = ? AND asset_id = ? AND deleted = 0 ORDER BY created_at ASC, id ASC`, userID, assetID)
}

func ListBookAnnotations(queryer Queryer, userID, bookID int64) ([]Annotation, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	return listAnnotations(queryer, `SELECT `+annotationColumns+` FROM user_annotations
        WHERE user_id = ? AND deleted = 0 AND asset_id IN (SELECT id FROM assets WHERE book_id = ?)
        ORDER BY asset_id, created_at, id`, userID, bookID)
}

func listAnnotations(queryer Queryer, query string, args ...any) ([]Annotation, error) {
	rows, err := queryer.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list annotations: %w", err)
	}
	defer rows.Close()
	out := []Annotation{}
	for rows.Next() {
		var ann Annotation
		if err := scanAnnotation(rows, &ann); err != nil {
			return nil, err
		}
		out = append(out, ann)
	}
	return out, rows.Err()
}

func (db *DB) CreateAnnotation(ctx context.Context, userID, assetID int64, input AnnotationCreate) (Annotation, error) {
	if userID <= 0 {
		return Annotation{}, ErrUserIDRequired
	}
	ann, err := normalizeAnnotation(userID, assetID, input)
	if err != nil {
		return Annotation{}, err
	}
	err = db.Transact(ctx, func(tx *Tx) error {
		if err := requireAsset(tx, assetID); err != nil {
			return err
		}
		var existing Annotation
		err := scanAnnotation(tx.QueryRow(`SELECT `+annotationColumns+` FROM user_annotations
			WHERE user_id = ? AND asset_id = ? AND coalesce(json_extract(locator, '$.cfi'), locator) = ? AND deleted = 0`, userID, assetID, annotationSelection(ann.Locator)), &existing)
		if err == nil {
			// Repeating a selection must not replace a note or color edited since
			// its creation, including when the original response was lost.
			ann = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return scanAnnotation(tx.QueryRow(`INSERT INTO user_annotations
            (user_id, asset_id, locator, quote, context_before, context_after, note, color)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+annotationColumns,
			ann.UserID, ann.AssetID, ann.Locator,
			ann.Quote, ann.ContextBefore, ann.ContextAfter, ann.Note, ann.Color), &ann)
	})
	return ann, err
}

func getAnnotation(queryer Queryer, userID, assetID, annotationID int64) (Annotation, error) {
	var ann Annotation
	err := scanAnnotation(queryer.QueryRow(`SELECT `+annotationColumns+` FROM user_annotations
        WHERE id = ? AND user_id = ? AND asset_id = ?`, annotationID, userID, assetID), &ann)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrAnnotationNotFound
	}
	return ann, err
}

func (db *DB) UpdateAnnotation(ctx context.Context, userID, assetID, annotationID int64, input AnnotationUpdate) (Annotation, error) {
	if userID <= 0 {
		return Annotation{}, ErrUserIDRequired
	}
	if input.Revision <= 0 {
		return Annotation{}, ErrInvalidAnnotation
	}
	if input.Note == nil && input.Color == nil {
		return Annotation{}, ErrInvalidAnnotation
	}
	if input.Note != nil && utf8.RuneCountInString(*input.Note) > MaxAnnotationNoteLength {
		return Annotation{}, errorWithDetail(ErrInvalidAnnotation, "This note is too long. Shorten it before saving.")
	}
	if input.Color != nil && !validAnnotationColor(*input.Color) {
		return Annotation{}, ErrInvalidAnnotation
	}
	var ann Annotation
	err := db.Transact(ctx, func(tx *Tx) error {
		var err error
		ann, err = getAnnotation(tx, userID, assetID, annotationID)
		if err != nil {
			return err
		}
		if ann.Deleted {
			return ErrReadingConflict
		}
		// An already-applied edit succeeds even with an old revision. Compare
		// only submitted fields; unrelated concurrent edits remain untouched.
		if (input.Note == nil || ann.Note == *input.Note) && (input.Color == nil || ann.Color == *input.Color) {
			return nil
		}
		if ann.Revision != input.Revision {
			return ErrReadingConflict
		}
		return scanAnnotation(tx.QueryRow(`UPDATE user_annotations SET note = coalesce(?, note),
            color = coalesce(?, color), revision = revision + 1, updated_at = unixepoch()
            WHERE id = ? AND user_id = ? AND asset_id = ? RETURNING `+annotationColumns,
			input.Note, input.Color, annotationID, userID, assetID), &ann)
	})
	return ann, err
}

func (db *DB) DeleteAnnotation(ctx context.Context, userID, assetID, annotationID, revision int64) error {
	if userID <= 0 {
		return ErrUserIDRequired
	}
	if revision <= 0 {
		return ErrInvalidAnnotation
	}
	return db.Transact(ctx, func(tx *Tx) error {
		ann, err := getAnnotation(tx, userID, assetID, annotationID)
		if err != nil {
			return err
		}
		if ann.Deleted {
			return nil
		}
		if ann.Revision != revision {
			return ErrReadingConflict
		}
		_, err = tx.Exec(`UPDATE user_annotations SET deleted = 1, revision = revision + 1,
            locator = '{}', quote = '', context_before = '', context_after = '', note = '',
            updated_at = unixepoch() WHERE id = ? AND user_id = ? AND asset_id = ?`, annotationID, userID, assetID)
		return err
	})
}

func scanAnnotation(scanner rowScanner, ann *Annotation) error {
	return scanner.Scan(&ann.ID, &ann.UserID, &ann.AssetID, &ann.Locator,
		&ann.Quote, &ann.ContextBefore, &ann.ContextAfter, &ann.Note, &ann.Color,
		&ann.Revision, &ann.Deleted, &ann.CreatedAt, &ann.UpdatedAt)
}

func normalizeAnnotation(userID, assetID int64, input AnnotationCreate) (Annotation, error) {
	color := strings.TrimSpace(input.Color)
	if color == "" {
		color = AnnotationColorYellow
	}
	ann := Annotation{UserID: userID, AssetID: assetID,
		Locator: input.Locator,
		Quote:   input.Quote, ContextBefore: input.ContextBefore, ContextAfter: input.ContextAfter,
		Note: input.Note, Color: color}
	if utf8.RuneCountInString(ann.Quote) > MaxAnnotationQuoteLength {
		return Annotation{}, errorWithDetail(ErrInvalidAnnotation, "This highlight is too long. Select a shorter passage.")
	}
	if utf8.RuneCountInString(ann.Note) > MaxAnnotationNoteLength {
		return Annotation{}, errorWithDetail(ErrInvalidAnnotation, "This note is too long. Shorten it before saving.")
	}
	var err error
	ann.Locator, err = normalizeLocator(ann.Locator)
	if err != nil || ann.Locator.CFI == "" && (ann.Locator.Page == 0 || len(ann.Locator.Rects) == 0) ||
		!validAnnotationColor(ann.Color) || strings.TrimSpace(ann.Quote) == "" ||
		utf8.RuneCountInString(ann.ContextBefore) > MaxAnnotationContextLength ||
		utf8.RuneCountInString(ann.ContextAfter) > MaxAnnotationContextLength {
		return Annotation{}, ErrInvalidAnnotation
	}
	return ann, nil
}

func validAnnotationColor(color string) bool {
	switch color {
	case AnnotationColorYellow, "green", "blue", "pink", "purple":
		return true
	}
	return false
}

// Matches the unique index. Resource paths do not change a CFI selection's identity.
func annotationSelection(locator Locator) any {
	if locator.CFI != "" {
		return locator.CFI
	}
	return locator
}
