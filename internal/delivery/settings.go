package delivery

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/levmv/polka/internal/appsettings"
)

const (
	smtpHostKey          = "email.smtp_host"
	smtpPortKey          = "email.smtp_port"
	smtpSecurityKey      = "email.smtp_security"
	smtpUsernameKey      = "email.smtp_username"
	smtpPasswordKey      = "email.smtp_password"
	fromAddressKey       = "email.from_address"
	fromNameKey          = "email.from_name"
	attachmentLimitMBKey = "email.attachment_limit_mb"
)

var (
	ErrInvalidSMTPPort        = errors.New("SMTP port is invalid")
	ErrInvalidSMTPSecurity    = errors.New("SMTP security must be auto, starttls, ssl, or plain")
	ErrInvalidAttachmentLimit = errors.New("Attachment limit must be between 1 and 200 MB")
	ErrInvalidFromAddress     = errors.New("From address is invalid")
)

// OpenSMTPConfig loads the library-wide email delivery settings. Missing rows
// use the same safe defaults as SMTPConfig.Normalized.
func OpenSMTPConfig(q appsettings.Queryer) (SMTPConfig, error) {
	var cfg SMTPConfig
	for _, field := range []struct {
		key  string
		dest *string
	}{
		{smtpHostKey, &cfg.Host},
		{smtpSecurityKey, &cfg.Security},
		{smtpUsernameKey, &cfg.Username},
		{smtpPasswordKey, &cfg.Password},
		{fromAddressKey, &cfg.FromAddress},
		{fromNameKey, &cfg.FromName},
	} {
		value, _, err := appsettings.Get(q, field.key)
		if err != nil {
			return SMTPConfig{}, fmt.Errorf("load SMTP setting %s: %w", field.key, err)
		}
		*field.dest = value
	}

	var err error
	cfg.Port, err = appsettings.GetInt(q, smtpPortKey, DefaultSMTPPort)
	if err != nil {
		return SMTPConfig{}, err
	}
	cfg.AttachmentLimitMB, err = appsettings.GetInt(q, attachmentLimitMBKey, DefaultAttachmentLimitMB)
	if err != nil {
		return SMTPConfig{}, err
	}
	return cfg.Normalized(), nil
}

// SaveSMTPConfig stores the complete configuration, including its password.
// Callers that need several keys to change atomically must pass a transaction.
func SaveSMTPConfig(exec appsettings.Execer, cfg SMTPConfig) error {
	return saveSMTPConfig(exec, cfg, true)
}

// SaveSMTPConfigKeepingPassword stores every setting except the password. It is
// intended for partial updates whose caller did not supply a new credential.
func SaveSMTPConfigKeepingPassword(exec appsettings.Execer, cfg SMTPConfig) error {
	return saveSMTPConfig(exec, cfg, false)
}

func saveSMTPConfig(exec appsettings.Execer, cfg SMTPConfig, savePassword bool) error {
	if err := ValidateSMTPConfig(cfg); err != nil {
		return err
	}
	cfg = cfg.Normalized()
	for _, field := range []struct {
		key   string
		value string
	}{
		{smtpHostKey, cfg.Host},
		{smtpSecurityKey, cfg.Security},
		{smtpUsernameKey, cfg.Username},
		{fromAddressKey, cfg.FromAddress},
		{fromNameKey, cfg.FromName},
	} {
		if err := appsettings.Set(exec, field.key, field.value); err != nil {
			return fmt.Errorf("save SMTP setting %s: %w", field.key, err)
		}
	}
	if err := appsettings.SetInt(exec, smtpPortKey, cfg.Port); err != nil {
		return err
	}
	if err := appsettings.SetInt(exec, attachmentLimitMBKey, cfg.AttachmentLimitMB); err != nil {
		return err
	}
	if savePassword {
		if err := appsettings.Set(exec, smtpPasswordKey, cfg.Password); err != nil {
			return fmt.Errorf("save SMTP setting %s: %w", smtpPasswordKey, err)
		}
	}
	return nil
}

func ValidateSMTPConfig(cfg SMTPConfig) error {
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return ErrInvalidSMTPPort
	}
	if !ValidSMTPSecurity(cfg.Security) {
		return ErrInvalidSMTPSecurity
	}
	if cfg.AttachmentLimitMB <= 0 || cfg.AttachmentLimitMB > 200 {
		return ErrInvalidAttachmentLimit
	}
	if strings.TrimSpace(cfg.FromAddress) != "" {
		addr, err := mail.ParseAddress(strings.TrimSpace(cfg.FromAddress))
		if err != nil || addr.Address == "" {
			return ErrInvalidFromAddress
		}
	}
	return nil
}
