package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
	"github.com/levmv/polka/internal/format"
)

const (
	deliveryJobDefaultLimit = 20
	deliveryJobMaxLimit     = 100

	deliveryMessageFailed            = "Delivery failed."
	deliveryMessagePrepareFailed     = "Could not prepare file for delivery."
	deliveryMessageFileMissing       = "File is no longer available."
	deliveryMessageNotConfigured     = "Email delivery is not configured"
	deliveryMessageNoLongerVisible   = "Book is no longer visible to this user."
	deliveryMessageConversionMissing = "Conversion is not available for this file."
	deliveryMessageConversionFailed  = "Conversion failed."
	deliveryMessageSendInterrupted   = "Delivery was interrupted while sending; the message may have been sent."
)

type EmailSettingsDTO struct {
	Configured        bool   `json:"configured"`
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Security          string `json:"security"`
	Username          string `json:"username"`
	PasswordSet       bool   `json:"password_set"`
	FromAddress       string `json:"from_address"`
	FromName          string `json:"from_name"`
	AttachmentLimitMB int    `json:"attachment_limit_mb"`
}

type emailSettingsRequest struct {
	Host              *string `json:"host"`
	Port              *int    `json:"port"`
	Security          *string `json:"security"`
	Username          *string `json:"username"`
	Password          *string `json:"password"`
	FromAddress       *string `json:"from_address"`
	FromName          *string `json:"from_name"`
	AttachmentLimitMB *int    `json:"attachment_limit_mb"`
}

type emailTestRequest struct {
	To string `json:"to"`
}

type DeliveryDeviceDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Preset    string `json:"preset"`
	IsDefault bool   `json:"is_default"`
	CreatedAt int64  `json:"created_at,omitzero"`
	UpdatedAt int64  `json:"updated_at,omitzero"`
}

type deliveryDeviceRequest struct {
	Name      *string `json:"name"`
	Email     *string `json:"email"`
	Preset    *string `json:"preset"`
	IsDefault *bool   `json:"is_default"`
}

type DeliveryPlanDTO struct {
	AssetID      string `json:"asset_id,omitempty"`
	Format       string `json:"format,omitempty"`
	Target       string `json:"target,omitempty"`
	Filename     string `json:"filename,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	SizeEstimate int64  `json:"size_estimate,omitzero"`
	Converted    bool   `json:"converted,omitzero"`
}

type DeliveryReasonDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SendOptionDTO struct {
	Device  DeliveryDeviceDTO  `json:"device"`
	Plan    *DeliveryPlanDTO   `json:"plan,omitzero"`
	Choices []DeliveryPlanDTO  `json:"choices,omitempty"`
	Reason  *DeliveryReasonDTO `json:"reason,omitzero"`
}

type SendOptionsDTO struct {
	Configured bool            `json:"configured"`
	Devices    []SendOptionDTO `json:"devices"`
	Reason     string          `json:"reason,omitempty"`
}

type createDeliveryRequest struct {
	WorkID   string `json:"work_id"`
	DeviceID string `json:"device_id"`
	AssetID  string `json:"asset_id"`
	Target   string `json:"target"`
}

type DeliveryJobDTO struct {
	ID          string `json:"id"`
	DeviceID    string `json:"device_id,omitempty"`
	DeviceName  string `json:"device_name"`
	DeviceEmail string `json:"device_email"`
	Preset      string `json:"preset"`
	WorkID      string `json:"work_id"`
	AssetID     string `json:"asset_id,omitempty"`
	Title       string `json:"title"`
	Target      string `json:"target,omitempty"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"size_bytes,omitzero"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	SentAt      int64  `json:"sent_at,omitzero"`
}

func (s *Server) handleAPIAdminEmail(w http.ResponseWriter, r *http.Request) {
	cfg, passwordSet, err := s.deliveryEmailConfig()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailSettingsDTO(cfg, passwordSet))
}

func (s *Server) handleAPIAdminEmailSave(w http.ResponseWriter, r *http.Request) {
	var req emailSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	cfg, _, err := s.deliveryEmailConfig()
	if err != nil {
		serverError(w, err)
		return
	}
	passwordChanged := req.Password != nil
	if req.Host != nil {
		cfg.Host = strings.TrimSpace(*req.Host)
	}
	if req.Port != nil {
		cfg.Port = *req.Port
	}
	if req.Security != nil {
		cfg.Security = strings.TrimSpace(*req.Security)
	}
	if req.Username != nil {
		cfg.Username = strings.TrimSpace(*req.Username)
	}
	if req.Password != nil {
		cfg.Password = *req.Password
	}
	if req.FromAddress != nil {
		cfg.FromAddress = strings.TrimSpace(*req.FromAddress)
	}
	if req.FromName != nil {
		cfg.FromName = strings.TrimSpace(*req.FromName)
	}
	if req.AttachmentLimitMB != nil {
		cfg.AttachmentLimitMB = *req.AttachmentLimitMB
	}
	if err := delivery.ValidateSMTPConfig(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg = cfg.Normalized()

	err = s.db.Transact(r.Context(), func(tx *sql.Tx) error {
		if passwordChanged {
			return delivery.SaveSMTPConfig(tx, cfg)
		}
		return delivery.SaveSMTPConfigKeepingPassword(tx, cfg)
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailSettingsDTO(cfg, cfg.Password != ""))
}

func (s *Server) handleAPIAdminEmailTest(w http.ResponseWriter, r *http.Request) {
	var req emailTestRequest
	if !readJSON(w, r, &req) {
		return
	}
	to, ok := normalizeEmailAddress(req.To)
	if !ok {
		http.Error(w, "Test recipient email is invalid", http.StatusBadRequest)
		return
	}
	cfg, _, err := s.deliveryEmailConfig()
	if err != nil {
		serverError(w, err)
		return
	}
	if !cfg.Configured() {
		http.Error(w, deliveryMessageNotConfigured, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := delivery.SendSMTPTest(ctx, cfg, to); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAPIDeliveryDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.ListDeliveryDevices(UserID(r.Context()))
	if writeDeliveryError(w, err) {
		return
	}
	out := make([]DeliveryDeviceDTO, 0, len(devices))
	for _, device := range devices {
		out = append(out, deliveryDeviceDTO(device))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIDeliveryDeviceCreate(w http.ResponseWriter, r *http.Request) {
	var req deliveryDeviceRequest
	if !readJSON(w, r, &req) {
		return
	}
	name := stringValue(req.Name)
	email, ok := normalizeEmailAddress(stringValue(req.Email))
	if !ok {
		http.Error(w, "Device email is invalid", http.StatusBadRequest)
		return
	}
	preset := stringValue(req.Preset)
	if preset == "" {
		preset = string(delivery.PresetFromEmail(email))
	}
	isDefault := boolValue(req.IsDefault)
	device, err := s.db.CreateDeliveryDevice(r.Context(), UserID(r.Context()), name, email, preset, isDefault)
	if writeDeliveryError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, deliveryDeviceDTO(*device))
}

func (s *Server) handleAPIDeliveryDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	current, err := s.db.GetDeliveryDevice(UserID(r.Context()), deviceID)
	if writeDeliveryError(w, err) {
		return
	}
	var req deliveryDeviceRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Email != nil {
		email, ok := normalizeEmailAddress(*req.Email)
		if !ok {
			http.Error(w, "Device email is invalid", http.StatusBadRequest)
			return
		}
		current.Email = email
	}
	if req.Preset != nil {
		current.Preset = strings.TrimSpace(*req.Preset)
	}
	if req.IsDefault != nil {
		current.IsDefault = *req.IsDefault
	}
	updated, err := s.db.UpdateDeliveryDevice(r.Context(), UserID(r.Context()), *current)
	if writeDeliveryError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, deliveryDeviceDTO(*updated))
}

func (s *Server) handleAPIDeliveryDeviceDelete(w http.ResponseWriter, r *http.Request) {
	err := s.db.DeleteDeliveryDevice(r.Context(), UserID(r.Context()), r.PathValue("id"))
	if writeDeliveryError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPISendOptions(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(r.URL.Query().Get("work"))
	if workID == "" {
		http.Error(w, "Missing work id", http.StatusBadRequest)
		return
	}
	cfg, _, err := s.deliveryEmailConfig()
	if err != nil {
		serverError(w, err)
		return
	}
	devices, err := s.db.ListDeliveryDevices(UserID(r.Context()))
	if writeDeliveryError(w, err) {
		return
	}
	if !cfg.Configured() {
		writeJSON(w, http.StatusOK, SendOptionsDTO{Configured: false, Devices: deviceOptionsWithoutPlans(devices), Reason: deliveryMessageNotConfigured})
		return
	}
	work, assets, ok := s.deliveryWorkForRequest(w, r, workID)
	if !ok {
		return
	}
	dwork := deliveryWork(work, assets)
	options := make([]SendOptionDTO, 0, len(devices))
	for _, device := range devices {
		planOpts := delivery.PlanOptions{
			Preset:            delivery.Preset(device.Preset),
			AttachmentLimitMB: cfg.AttachmentLimitMB,
		}
		plan := delivery.PlanDelivery(dwork, planOpts)
		choices := delivery.PlanChoices(dwork, planOpts)
		options = append(options, sendOptionDTO(device, plan, choices))
	}
	writeJSON(w, http.StatusOK, SendOptionsDTO{Configured: true, Devices: options})
}

func (s *Server) handleAPIDeliveryCreate(w http.ResponseWriter, r *http.Request) {
	var req createDeliveryRequest
	if !readJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.WorkID) == "" {
		http.Error(w, "Missing work id", http.StatusBadRequest)
		return
	}
	cfg, _, err := s.deliveryEmailConfig()
	if err != nil {
		serverError(w, err)
		return
	}
	if !cfg.Configured() {
		http.Error(w, deliveryMessageNotConfigured, http.StatusBadRequest)
		return
	}

	var device *db.DeliveryDevice
	if strings.TrimSpace(req.DeviceID) != "" {
		device, err = s.db.GetDeliveryDevice(UserID(r.Context()), req.DeviceID)
	} else {
		device, err = s.db.DefaultDeliveryDevice(UserID(r.Context()))
	}
	if writeDeliveryError(w, err) {
		return
	}
	work, assets, ok := s.deliveryWorkForRequest(w, r, req.WorkID)
	if !ok {
		return
	}
	plan := delivery.PlanDelivery(deliveryWork(work, assets), delivery.PlanOptions{
		Preset:            delivery.Preset(device.Preset),
		AttachmentLimitMB: cfg.AttachmentLimitMB,
		RequestedAssetID:  strings.TrimSpace(req.AssetID),
		RequestedTarget:   converter.Target(req.Target),
	})
	if !plan.Sendable() {
		http.Error(w, plan.Reason.Message, http.StatusUnprocessableEntity)
		return
	}
	job, err := s.db.CreateDeliveryJob(db.DeliveryJob{
		UserID:      UserID(r.Context()),
		DeviceID:    sql.NullString{String: device.ID, Valid: true},
		DeviceName:  device.Name,
		DeviceEmail: device.Email,
		Preset:      device.Preset,
		WorkID:      work.ID,
		AssetID:     sql.NullString{String: plan.AssetID, Valid: true},
		Title:       work.Title,
		Target:      sql.NullString{String: string(plan.Target), Valid: plan.Target != ""},
		Filename:    plan.Filename,
		SizeBytes:   sql.NullInt64{Int64: plan.SizeBytes, Valid: plan.SizeBytes > 0},
	})
	if err != nil {
		serverError(w, err)
		return
	}
	s.wakeDeliveryWorker()
	writeJSON(w, http.StatusAccepted, deliveryJobDTO(*job))
}

func (s *Server) handleAPIDeliveries(w http.ResponseWriter, r *http.Request) {
	limit := deliveryJobDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = min(n, deliveryJobMaxLimit)
		}
	}
	jobs, err := s.db.ListDeliveryJobs(UserID(r.Context()), limit)
	if writeDeliveryError(w, err) {
		return
	}
	out := make([]DeliveryJobDTO, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, deliveryJobDTO(job))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIDelivery(w http.ResponseWriter, r *http.Request) {
	job, err := s.db.GetDeliveryJob(UserID(r.Context()), r.PathValue("id"))
	if writeDeliveryError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, deliveryJobDTO(*job))
}

func (s *Server) deliveryEmailConfig() (delivery.SMTPConfig, bool, error) {
	cfg, err := delivery.OpenSMTPConfig(s.db)
	if err != nil {
		return delivery.SMTPConfig{}, false, err
	}
	return cfg, cfg.Password != "", nil
}

func emailSettingsDTO(cfg delivery.SMTPConfig, passwordSet bool) EmailSettingsDTO {
	return EmailSettingsDTO{
		Configured:        cfg.Configured(),
		Host:              cfg.Host,
		Port:              cfg.Port,
		Security:          cfg.Security,
		Username:          cfg.Username,
		PasswordSet:       passwordSet,
		FromAddress:       cfg.FromAddress,
		FromName:          cfg.FromName,
		AttachmentLimitMB: cfg.AttachmentLimitMB,
	}
}

func deliveryDeviceDTO(device db.DeliveryDevice) DeliveryDeviceDTO {
	return DeliveryDeviceDTO{
		ID:        device.ID,
		Name:      device.Name,
		Email:     device.Email,
		Preset:    device.Preset,
		IsDefault: device.IsDefault,
		CreatedAt: device.CreatedAt,
		UpdatedAt: device.UpdatedAt,
	}
}

func sendOptionDTO(device db.DeliveryDevice, plan delivery.Plan, choices []delivery.PlanChoice) SendOptionDTO {
	dto := SendOptionDTO{Device: deliveryDeviceDTO(device)}
	if plan.Sendable() {
		planDTO := deliveryPlanDTO(plan)
		dto.Plan = &planDTO
	} else {
		dto.Reason = &DeliveryReasonDTO{Code: plan.Reason.Code, Message: plan.Reason.Message}
	}
	dto.Choices = deliveryChoiceDTOs(choices)
	return dto
}

func deliveryChoiceDTOs(choices []delivery.PlanChoice) []DeliveryPlanDTO {
	if len(choices) == 0 {
		return nil
	}
	out := make([]DeliveryPlanDTO, 0, len(choices))
	for _, choice := range choices {
		out = append(out, deliveryPlanDTO(choice.Plan))
	}
	return out
}

func deliveryPlanDTO(plan delivery.Plan) DeliveryPlanDTO {
	return DeliveryPlanDTO{
		AssetID:      plan.AssetID,
		Format:       format.FormatKey(plan.SourceFormat),
		Target:       string(plan.Target),
		Filename:     plan.Filename,
		MediaType:    plan.MediaType,
		SizeEstimate: plan.SizeBytes,
		Converted:    plan.Converted,
	}
}

func deliveryJobDTO(job db.DeliveryJob) DeliveryJobDTO {
	dto := DeliveryJobDTO{
		ID:          job.ID,
		DeviceName:  job.DeviceName,
		DeviceEmail: job.DeviceEmail,
		Preset:      job.Preset,
		WorkID:      job.WorkID,
		Title:       job.Title,
		Filename:    job.Filename,
		Status:      job.Status,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,
	}
	if job.DeviceID.Valid {
		dto.DeviceID = job.DeviceID.String
	}
	if job.AssetID.Valid {
		dto.AssetID = job.AssetID.String
	}
	if job.Target.Valid {
		dto.Target = job.Target.String
	}
	if job.SizeBytes.Valid {
		dto.SizeBytes = job.SizeBytes.Int64
	}
	if job.SentAt.Valid {
		dto.SentAt = job.SentAt.Int64
	}
	return dto
}

func deviceOptionsWithoutPlans(devices []db.DeliveryDevice) []SendOptionDTO {
	options := make([]SendOptionDTO, 0, len(devices))
	for _, device := range devices {
		options = append(options, SendOptionDTO{Device: deliveryDeviceDTO(device)})
	}
	return options
}

func (s *Server) deliveryWorkForRequest(w http.ResponseWriter, r *http.Request, workID string) (db.DeliveryWorkRow, []db.DeliveryAssetRow, bool) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return db.DeliveryWorkRow{}, nil, false
	}
	work, assets, err := s.db.DeliveryWorkForPlan(scope, workID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book not found", http.StatusNotFound)
		return db.DeliveryWorkRow{}, nil, false
	}
	if err != nil {
		serverError(w, err)
		return db.DeliveryWorkRow{}, nil, false
	}
	return work, assets, true
}

func deliveryWork(work db.DeliveryWorkRow, assets []db.DeliveryAssetRow) delivery.Work {
	out := delivery.Work{
		ID:      work.ID,
		Title:   work.Title,
		Authors: work.Authors,
		Assets:  make([]delivery.Asset, 0, len(assets)),
	}
	for _, asset := range assets {
		out.Assets = append(out.Assets, delivery.Asset{
			ID:        asset.ID,
			Filename:  asset.Filename,
			Extension: asset.Extension,
			Format:    asset.Format,
			Size:      asset.Size,
			IsPrimary: asset.IsPrimary,
		})
	}
	return out
}

func normalizeEmailAddress(raw string) (string, bool) {
	addr, err := mail.ParseAddress(strings.TrimSpace(raw))
	if err != nil || addr.Address == "" {
		return "", false
	}
	return addr.Address, true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func writeDeliveryError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, db.ErrDeliveryDeviceNotFound):
		http.Error(w, "Delivery device not found", http.StatusNotFound)
	case errors.Is(err, db.ErrDeliveryJobNotFound):
		http.Error(w, "Delivery job not found", http.StatusNotFound)
	case errors.Is(err, db.ErrDeliveryDeviceNameExists):
		http.Error(w, "A delivery device with this name already exists", http.StatusConflict)
	case errors.Is(err, db.ErrDeliveryDeviceNameMissing),
		errors.Is(err, db.ErrDeliveryDeviceEmailMissing),
		errors.Is(err, db.ErrInvalidDeliveryPreset):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, err)
	}
	return true
}
