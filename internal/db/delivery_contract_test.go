package db_test

import (
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/delivery"
)

func TestDeliveryPresetStorageContractMatchesPolicy(t *testing.T) {
	for _, test := range []struct {
		stored string
		policy delivery.Preset
	}{
		{stored: db.DeliveryPresetKindle, policy: delivery.PresetKindle},
		{stored: db.DeliveryPresetPocketBook, policy: delivery.PresetPocketBook},
		{stored: db.DeliveryPresetGeneric, policy: delivery.PresetGeneric},
	} {
		if test.stored != string(test.policy) {
			t.Errorf("stored preset %q != policy preset %q", test.stored, test.policy)
		}
		if !db.ValidDeliveryPreset(test.stored) || !delivery.ValidPreset(test.policy) {
			t.Errorf("preset %q must be valid in storage and policy", test.stored)
		}
	}
}
