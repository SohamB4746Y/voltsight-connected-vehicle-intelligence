// Package normalise turns raw OEM-cloud payloads (two JSON dialects plus the platform's own protobuf)
// into the platform's TelemetryEvent contract and validates them. Every rejection carries the DLQ
// error class it belongs to.
package normalise

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// dtcRe is the textual OBD-II diagnostic trouble code form: a system letter (P powertrain, C chassis,
// B body, U network), a digit 0-3 (SAE generic vs manufacturer-specific) and three hex digits.
var dtcRe = regexp.MustCompile(`^[PCBU][0-3][0-9A-F]{3}$`)

// ErrBadDTC is returned for codes that are not valid OBD-II DTCs.
var ErrBadDTC = errors.New("invalid OBD-II DTC")

// ValidDTC reports whether s is a well-formed textual OBD-II code such as "P0301".
func ValidDTC(s string) bool { return dtcRe.MatchString(s) }

// DecodeDTCRaw decodes the on-the-wire two-byte OBD-II form given as four hex digits (for example
// "0301" -> P0301, "C123" -> U0123 style per SAE J1979): bits 15-14 select the system letter, bits
// 13-12 the first digit, the remaining 12 bits are three hex digits.
func DecodeDTCRaw(raw string) (string, error) {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if len(raw) != 4 {
		return "", fmt.Errorf("%w: raw code %q must be 4 hex digits", ErrBadDTC, raw)
	}
	var v uint16
	for i := 0; i < 4; i++ {
		c := raw[i]
		var n uint16
		switch {
		case c >= '0' && c <= '9':
			n = uint16(c - '0')
		case c >= 'A' && c <= 'F':
			n = uint16(c-'A') + 10
		default:
			return "", fmt.Errorf("%w: non-hex character in %q", ErrBadDTC, raw)
		}
		v = v<<4 | n
	}
	letter := "PCBU"[v>>14]
	return fmt.Sprintf("%c%d%03X", letter, (v>>12)&3, v&0xfff), nil
}

// EncodeDTCRaw is the inverse of DecodeDTCRaw.
func EncodeDTCRaw(code string) (string, error) {
	if !ValidDTC(code) {
		return "", fmt.Errorf("%w: %q", ErrBadDTC, code)
	}
	letter := strings.IndexByte("PCBU", code[0])
	var rest uint16
	for _, c := range code[1:] {
		var n uint16
		if c >= '0' && c <= '9' {
			n = uint16(c - '0')
		} else {
			n = uint16(c-'A') + 10
		}
		rest = rest<<4 | n
	}
	// rest holds 4 nibbles: first digit (0-3) in the top nibble, then three hex digits
	v := uint16(letter)<<14 | (rest & 0x3fff)
	return fmt.Sprintf("%04X", v), nil
}

// ParseDTCList decodes the raw hex field of OEM B: zero or more concatenated 4-hex-digit codes.
func ParseDTCList(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("%w: %q is not a multiple of 4 hex digits", ErrBadDTC, raw)
	}
	var out []string
	for i := 0; i < len(raw); i += 4 {
		c, err := DecodeDTCRaw(raw[i : i+4])
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
