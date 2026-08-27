// Package id owns sortable identifiers for persisted Polka entities.
package id

import (
	"crypto/rand"
	"encoding/base32"
	"time"
)

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// Kind identifies the persisted entity named by a Polka ID prefix.
type Kind string

const (
	Work               Kind = "w_"
	Asset              Kind = "a_"
	Author             Kind = "au_"
	User               Kind = "u_"
	AppToken           Kind = "t_"
	Shelf              Kind = "s_"
	KoboConnection     Kind = "kc_"
	Annotation         Kind = "ann_"
	ReadingStatusEvent Kind = "rse_"
	DuplicateDismissal Kind = "dupd_"
	DeliveryDevice     Kind = "dd_"
	DeliveryJob        Kind = "dj_"
)

// New generates a ULID-style sortable identifier of the given kind.
func New(kind Kind) string {
	now := time.Now().UnixMilli()
	var entropy [10]byte
	rand.Read(entropy[:])

	var data [16]byte
	data[0] = byte(now >> 40)
	data[1] = byte(now >> 32)
	data[2] = byte(now >> 24)
	data[3] = byte(now >> 16)
	data[4] = byte(now >> 8)
	data[5] = byte(now)
	copy(data[6:], entropy[:])

	return string(kind) + crockford.EncodeToString(data[:])
}
