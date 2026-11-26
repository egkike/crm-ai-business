package usecase

import (
	"crypto/rand"
	"fmt"
)

// newUUID generates an internal UUID v4 primary key.
//
// It uses crypto/rand directly: the product needs one UUID shape and nothing
// else, and a dependency for sixteen random bytes would be a dependency to vet,
// pin and re-vet on every advisory. The layout is RFC 4122 version 4 (version
// and variant bits set), so the value stays interchangeable with the fixed UUIDs
// the schema seeds (db.TicketTypeConsultaID) and with the clients rows the
// registration seam creates.
//
// Every business entity of this project uses an internal UUID primary key
// (PRD §3.7), so the generator lives here once instead of being copied per use
// case.
func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("no se pudo generar el id interno: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC 4122 variant

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
