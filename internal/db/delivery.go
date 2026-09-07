package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/levmv/polka/internal/format"
)

const (
	DeliveryPresetKindle     = "kindle"
	DeliveryPresetPocketBook = "pocketbook"
	DeliveryPresetGeneric    = "generic"

	DeliveryStatusQueued     = "queued"
	DeliveryStatusConverting = "converting"
	DeliveryStatusSending    = "sending"
	DeliveryStatusSent       = "sent"
	DeliveryStatusFailed     = "failed"
)

const (
	deliveryDeviceColumns = `id, user_id, name, email, preset, is_default, created_at, updated_at`
	deliveryJobColumns    = `id, user_id, device_id, device_name, device_email, preset, book_id,
		asset_id, title, target, filename, size_bytes, status, error,
		created_at, updated_at, sent_at`
)

var (
	ErrDeliveryDeviceNotFound     = errors.New("delivery device not found")
	ErrDeliveryDeviceNameExists   = errors.New("delivery device name already exists")
	ErrDeliveryDeviceNameMissing  = errors.New("delivery device name is required")
	ErrDeliveryDeviceEmailMissing = errors.New("delivery device email is required")
	ErrInvalidDeliveryPreset      = fmt.Errorf("delivery preset must be %s, %s, or %s", DeliveryPresetKindle, DeliveryPresetPocketBook, DeliveryPresetGeneric)
	ErrDeliveryJobNotFound        = errors.New("delivery job not found")
)

type DeliveryDevice struct {
	ID        int64
	UserID    int64
	Name      string
	Email     string
	Preset    string
	IsDefault bool
	CreatedAt int64
	UpdatedAt int64
}

type DeliveryJob struct {
	ID          int64
	UserID      int64
	DeviceID    sql.NullInt64
	DeviceName  string
	DeviceEmail string
	Preset      string
	BookID      int64
	AssetID     sql.NullInt64
	Title       string
	Target      sql.NullString
	Filename    string
	SizeBytes   sql.NullInt64
	Status      string
	Error       string
	CreatedAt   int64
	UpdatedAt   int64
	SentAt      sql.NullInt64
}

type DeliveryBookRow struct {
	ID      int64
	Title   string
	Authors string
}

type DeliveryAssetRow struct {
	ID        int64
	Filename  string
	Extension string
	Format    format.Format
	Size      int64
	IsPrimary bool
}

func ValidDeliveryPreset(preset string) bool {
	switch preset {
	case DeliveryPresetKindle, DeliveryPresetPocketBook, DeliveryPresetGeneric:
		return true
	default:
		return false
	}
}

func ListDeliveryDevices(queryer Queryer, userID int64) ([]DeliveryDevice, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	rows, err := queryer.Query(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ?
		ORDER BY is_default DESC, name COLLATE NOCASE ASC, created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list delivery devices: %w", err)
	}
	defer rows.Close()

	var devices []DeliveryDevice
	for rows.Next() {
		device, err := scanDeliveryDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func GetDeliveryDevice(queryer Queryer, userID, deviceID int64) (*DeliveryDevice, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	device, err := scanDeliveryDevice(queryer.QueryRow(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ? AND id = ?
	`, userID, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery device: %w", err)
	}
	return &device, nil
}

func DefaultDeliveryDevice(queryer Queryer, userID int64) (*DeliveryDevice, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	device, err := scanDeliveryDevice(queryer.QueryRow(`
		SELECT `+deliveryDeviceColumns+`
		FROM delivery_devices
		WHERE user_id = ?
		ORDER BY is_default DESC, created_at DESC
		LIMIT 1
	`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get default delivery device: %w", err)
	}
	return &device, nil
}

func (db *DB) CreateDeliveryDevice(ctx context.Context, userID int64, name, email, preset string, isDefault bool) (*DeliveryDevice, error) {
	if err := validateDeliveryDeviceInput(userID, name, email, preset); err != nil {
		return nil, err
	}
	var device DeliveryDevice
	err := db.Transact(ctx, func(tx *Tx) error {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM delivery_devices WHERE user_id = ?", userID).Scan(&count); err != nil {
			return fmt.Errorf("count delivery devices: %w", err)
		}
		if count == 0 {
			isDefault = true
		}
		if isDefault {
			if _, err := tx.Exec("UPDATE delivery_devices SET is_default = 0, updated_at = unixepoch() WHERE user_id = ?", userID); err != nil {
				return fmt.Errorf("clear delivery default: %w", err)
			}
		}
		var err error
		device, err = scanDeliveryDevice(tx.QueryRow(`
			INSERT INTO delivery_devices (user_id, name, email, preset, is_default)
			VALUES (?, ?, ?, ?, ?)
			RETURNING `+deliveryDeviceColumns,
			userID, strings.TrimSpace(name), strings.TrimSpace(email), preset, boolInt(isDefault)))
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDeliveryDeviceNameExists
			}
			return fmt.Errorf("insert delivery device: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &device, nil
}

func (db *DB) UpdateDeliveryDevice(ctx context.Context, userID int64, device DeliveryDevice) (*DeliveryDevice, error) {
	if device.ID <= 0 {
		return nil, ErrDeliveryDeviceNotFound
	}
	if err := validateDeliveryDeviceInput(userID, device.Name, device.Email, device.Preset); err != nil {
		return nil, err
	}
	err := db.Transact(ctx, func(tx *Tx) error {
		if device.IsDefault {
			if _, err := tx.Exec("UPDATE delivery_devices SET is_default = 0, updated_at = unixepoch() WHERE user_id = ?", userID); err != nil {
				return fmt.Errorf("clear delivery default: %w", err)
			}
		}
		res, err := tx.Exec(`
			UPDATE delivery_devices
			SET name = ?, email = ?, preset = ?, is_default = ?, updated_at = unixepoch()
			WHERE user_id = ? AND id = ?
		`, strings.TrimSpace(device.Name), strings.TrimSpace(device.Email), device.Preset, boolInt(device.IsDefault), userID, device.ID)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrDeliveryDeviceNameExists
			}
			return fmt.Errorf("update delivery device: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeliveryDeviceNotFound
		}
		if !device.IsDefault {
			if err := ensureDeliveryDefault(tx, userID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetDeliveryDevice(db.Read(ctx), userID, device.ID)
}

func (db *DB) DeleteDeliveryDevice(ctx context.Context, userID, deviceID int64) error {
	if userID <= 0 {
		return ErrUserIDRequired
	}
	return db.Transact(ctx, func(tx *Tx) error {
		res, err := tx.Exec("DELETE FROM delivery_devices WHERE user_id = ? AND id = ?", userID, deviceID)
		if err != nil {
			return fmt.Errorf("delete delivery device: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrDeliveryDeviceNotFound
		}
		return ensureDeliveryDefault(tx, userID)
	})
}

func ensureDeliveryDefault(tx *Tx, userID int64) error {
	var defaults int
	if err := tx.QueryRow("SELECT COUNT(*) FROM delivery_devices WHERE user_id = ? AND is_default = 1", userID).Scan(&defaults); err != nil {
		return fmt.Errorf("count delivery defaults: %w", err)
	}
	if defaults > 0 {
		return nil
	}
	_, err := tx.Exec(`
		UPDATE delivery_devices
		SET is_default = 1, updated_at = unixepoch()
		WHERE id = (
			SELECT id FROM delivery_devices
			WHERE user_id = ?
			ORDER BY created_at DESC
			LIMIT 1
		)
	`, userID)
	if err != nil {
		return fmt.Errorf("promote delivery default: %w", err)
	}
	return nil
}

func validateDeliveryDeviceInput(userID int64, name, email, preset string) error {
	if userID <= 0 {
		return ErrUserIDRequired
	}
	if strings.TrimSpace(name) == "" {
		return ErrDeliveryDeviceNameMissing
	}
	if strings.TrimSpace(email) == "" {
		return ErrDeliveryDeviceEmailMissing
	}
	if !ValidDeliveryPreset(preset) {
		return ErrInvalidDeliveryPreset
	}
	return nil
}

func DeliveryBookForPlan(queryer Queryer, scope VisibilityScope, bookID int64) (DeliveryBookRow, []DeliveryAssetRow, error) {
	where, args := scope.AppendBookWhere("b.id = ? AND b.deleted_at IS NULL", "b.id", bookID)
	var book DeliveryBookRow
	err := queryer.QueryRow(`
		SELECT b.id, b.title, `+colAuthors+`
		FROM books b
		WHERE `+where+`
		LIMIT 1
	`, args...).Scan(&book.ID, &book.Title, &book.Authors)
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryBookRow{}, nil, sql.ErrNoRows
	}
	if err != nil {
		return DeliveryBookRow{}, nil, fmt.Errorf("get delivery book: %w", err)
	}

	rows, err := queryer.Query(`
		SELECT id, filename, extension, format, COALESCE(current_size, original_size, 0), is_primary
		FROM assets
		WHERE book_id = ?
		ORDER BY is_primary DESC, id ASC
	`, book.ID)
	if err != nil {
		return DeliveryBookRow{}, nil, fmt.Errorf("list delivery assets: %w", err)
	}
	defer rows.Close()

	var assets []DeliveryAssetRow
	for rows.Next() {
		var row DeliveryAssetRow
		var formatKey string
		var isPrimary int
		if err := rows.Scan(&row.ID, &row.Filename, &row.Extension, &formatKey, &row.Size, &isPrimary); err != nil {
			return DeliveryBookRow{}, nil, fmt.Errorf("scan delivery asset: %w", err)
		}
		row.Format = format.FormatFromKey(formatKey)
		row.IsPrimary = isPrimary != 0
		assets = append(assets, row)
	}
	if err := rows.Err(); err != nil {
		return DeliveryBookRow{}, nil, err
	}
	return book, assets, nil
}

func (db *DB) CreateDeliveryJob(ctx context.Context, job DeliveryJob) (*DeliveryJob, error) {
	if job.Status == "" {
		job.Status = DeliveryStatusQueued
	}
	var saved *DeliveryJob
	err := db.Transact(ctx, func(tx *Tx) error {
		var err error
		saved, err = scanDeliveryJobRow(tx.QueryRow(`
			INSERT INTO delivery_jobs (
				user_id, device_id, device_name, device_email, preset, book_id,
				asset_id, title, target, filename, size_bytes, status, error
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING `+deliveryJobColumns,
			job.UserID, job.DeviceID, job.DeviceName, job.DeviceEmail, job.Preset,
			job.BookID, job.AssetID, job.Title, job.Target, job.Filename, job.SizeBytes,
			job.Status, job.Error))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create delivery job: %w", err)
	}
	return saved, nil
}

func GetDeliveryJob(queryer Queryer, userID, jobID int64) (*DeliveryJob, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	job, err := scanDeliveryJobRow(queryer.QueryRow(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE user_id = ? AND id = ?
	`, userID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery job: %w", err)
	}
	return job, nil
}

func GetDeliveryJobByID(queryer Queryer, jobID int64) (*DeliveryJob, error) {
	job, err := scanDeliveryJobRow(queryer.QueryRow(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE id = ?
	`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeliveryJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get delivery job by id: %w", err)
	}
	return job, nil
}

// NextQueuedDeliveryJob returns the oldest durable delivery waiting for the
// single server worker. The writer lease guarantees there is only one worker
// process, so a separate claim/lock protocol would add no useful safety here.
func NextQueuedDeliveryJob(queryer Queryer) (*DeliveryJob, error) {
	job, err := scanDeliveryJobRow(queryer.QueryRow(`
		SELECT ` + deliveryJobColumns + `
		FROM delivery_jobs
		WHERE status = 'queued'
		ORDER BY created_at ASC, id ASC
		LIMIT 1
	`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get next queued delivery job: %w", err)
	}
	return job, nil
}

func ListDeliveryJobs(queryer Queryer, userID int64, limit int) ([]DeliveryJob, error) {
	if userID <= 0 {
		return nil, ErrUserIDRequired
	}
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	rows, err := queryer.Query(`
		SELECT `+deliveryJobColumns+`
		FROM delivery_jobs
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list delivery jobs: %w", err)
	}
	defer rows.Close()

	var jobs []DeliveryJob
	for rows.Next() {
		job, err := scanDeliveryJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (db *DB) SetDeliveryJobStatus(ctx context.Context, jobID int64, status, errorMessage string) error {
	if !validDeliveryStatus(status) {
		return fmt.Errorf("invalid delivery status %q", status)
	}
	sentExpr := "sent_at"
	if status == DeliveryStatusSent {
		sentExpr = "unixepoch()"
	}
	res, err := db.Write(ctx).Exec(`
		UPDATE delivery_jobs
		SET status = ?, error = ?, updated_at = unixepoch(), sent_at = `+sentExpr+`
		WHERE id = ?
	`, status, errorMessage, jobID)
	if err != nil {
		return fmt.Errorf("set delivery job status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDeliveryJobNotFound
	}
	return nil
}

func (db *DB) SetDeliveryJobSize(ctx context.Context, jobID, size int64) error {
	_, err := db.Write(ctx).Exec("UPDATE delivery_jobs SET size_bytes = ?, updated_at = unixepoch() WHERE id = ?", size, jobID)
	if err != nil {
		return fmt.Errorf("set delivery job size: %w", err)
	}
	return nil
}

// RecoverDeliveryJobs preserves work which is safe to repeat while refusing to
// automatically duplicate a possibly completed SMTP send.
func (db *DB) RecoverDeliveryJobs(ctx context.Context) error {
	_, err := db.Write(ctx).Exec(`
		UPDATE delivery_jobs
		SET status = CASE status
		        WHEN 'converting' THEN 'queued'
		        ELSE 'failed'
		    END,
		    error = CASE status
		        WHEN 'converting' THEN ''
		        ELSE 'Delivery was interrupted while sending; the message may have been sent.'
		    END,
		    updated_at = unixepoch()
		WHERE status IN ('converting', 'sending')
	`)
	if err != nil {
		return fmt.Errorf("recover delivery jobs: %w", err)
	}
	return nil
}

func validDeliveryStatus(status string) bool {
	switch status {
	case DeliveryStatusQueued, DeliveryStatusConverting, DeliveryStatusSending, DeliveryStatusSent, DeliveryStatusFailed:
		return true
	default:
		return false
	}
}

func scanDeliveryDevice(row rowScanner) (DeliveryDevice, error) {
	var device DeliveryDevice
	var isDefault int
	if err := row.Scan(&device.ID, &device.UserID, &device.Name, &device.Email, &device.Preset, &isDefault, &device.CreatedAt, &device.UpdatedAt); err != nil {
		return DeliveryDevice{}, fmt.Errorf("scan delivery device: %w", err)
	}
	device.IsDefault = isDefault != 0
	return device, nil
}

func scanDeliveryJobRow(row rowScanner) (*DeliveryJob, error) {
	var job DeliveryJob
	if err := row.Scan(
		&job.ID, &job.UserID, &job.DeviceID, &job.DeviceName, &job.DeviceEmail,
		&job.Preset, &job.BookID, &job.AssetID, &job.Title, &job.Target,
		&job.Filename, &job.SizeBytes, &job.Status, &job.Error,
		&job.CreatedAt, &job.UpdatedAt, &job.SentAt,
	); err != nil {
		return nil, err
	}
	return &job, nil
}

func scanDeliveryJob(rows *sql.Rows) (DeliveryJob, error) {
	job, err := scanDeliveryJobRow(rows)
	if err != nil {
		return DeliveryJob{}, fmt.Errorf("scan delivery job: %w", err)
	}
	return *job, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
