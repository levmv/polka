package delivery_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
)

func TestSMTPSettingsRoundTripKeepsPasswordSeparate(t *testing.T) {
	database, err := db.InitPath(filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatalf("db.InitPath: %v", err)
	}
	defer database.Close()

	defaults, err := delivery.OpenSMTPConfig(database)
	if err != nil {
		t.Fatalf("open defaults: %v", err)
	}
	if defaults.Port != delivery.DefaultSMTPPort || defaults.Security != delivery.SMTPSecurityAuto || defaults.AttachmentLimitMB != delivery.DefaultAttachmentLimitMB {
		t.Fatalf("defaults = %+v", defaults)
	}

	if err := delivery.SaveSMTPConfig(database, delivery.SMTPConfig{
		Host:              " smtp.example.org ",
		Port:              465,
		Security:          delivery.SMTPSecuritySSL,
		Username:          " books ",
		Password:          "secret",
		FromAddress:       "books@example.org",
		FromName:          " Polka ",
		AttachmentLimitMB: 40,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := delivery.SaveSMTPConfigKeepingPassword(database, delivery.SMTPConfig{
		Host:              " smtp.example.org ",
		Port:              465,
		Security:          delivery.SMTPSecuritySSL,
		Username:          " books ",
		Password:          "stale-value-must-not-be-written",
		FromAddress:       "books@example.org",
		FromName:          " Polka ",
		AttachmentLimitMB: 40,
	}); err != nil {
		t.Fatalf("save config keeping password: %v", err)
	}

	got, err := delivery.OpenSMTPConfig(database)
	if err != nil {
		t.Fatalf("open saved config: %v", err)
	}
	if got.Host != "smtp.example.org" || got.Port != 465 || got.Security != delivery.SMTPSecuritySSL || got.Username != "books" || got.FromName != "Polka" || got.AttachmentLimitMB != 40 {
		t.Fatalf("saved config = %+v", got)
	}
	if got.Password != "secret" {
		t.Fatalf("password = %q; want preserved secret", got.Password)
	}
}

func TestValidateSMTPConfig(t *testing.T) {
	valid := delivery.SMTPConfig{
		Port:              delivery.DefaultSMTPPort,
		Security:          delivery.SMTPSecurityAuto,
		FromAddress:       "books@example.org",
		AttachmentLimitMB: delivery.DefaultAttachmentLimitMB,
	}
	for _, test := range []struct {
		name string
		edit func(*delivery.SMTPConfig)
		want error
	}{
		{name: "port", edit: func(c *delivery.SMTPConfig) { c.Port = 0 }, want: delivery.ErrInvalidSMTPPort},
		{name: "security", edit: func(c *delivery.SMTPConfig) { c.Security = "tls-ish" }, want: delivery.ErrInvalidSMTPSecurity},
		{name: "attachment limit", edit: func(c *delivery.SMTPConfig) { c.AttachmentLimitMB = 201 }, want: delivery.ErrInvalidAttachmentLimit},
		{name: "from address", edit: func(c *delivery.SMTPConfig) { c.FromAddress = "not an address" }, want: delivery.ErrInvalidFromAddress},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid
			test.edit(&cfg)
			if err := delivery.ValidateSMTPConfig(cfg); !errors.Is(err, test.want) {
				t.Fatalf("ValidateSMTPConfig error = %v; want %v", err, test.want)
			}
		})
	}
}
