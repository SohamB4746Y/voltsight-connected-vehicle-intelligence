// Package vin validates and builds Vehicle Identification Numbers per ISO 3779
// (17 characters from [A-HJ-NPR-Z0-9], no I/O/Q, check digit at position 9).
package vin

import (
	"errors"
	"strings"
)

const (
	// Length is the fixed VIN length.
	Length        = 17
	checkPosition = 8 // zero-based index of the check digit
	alphabet      = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"
)

var (
	letters = "ABCDEFGHJKLMNPRSTUVWXYZ"
	// transliteration values for the letters above, in order.
	letterValues = [...]int{1, 2, 3, 4, 5, 6, 7, 8, 1, 2, 3, 4, 5, 7, 9, 2, 3, 4, 5, 6, 7, 8, 9}
	weights      = [Length]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}
)

// Errors returned by Check.
var (
	ErrLength      = errors.New("vin: must be 17 characters")
	ErrCharacter   = errors.New("vin: contains an invalid character (I, O, Q and non-alphanumerics are not allowed)")
	ErrCheckDigit  = errors.New("vin: check digit mismatch")
	ErrBadTemplate = errors.New("vin: template must be 17 characters with 'x' at the check position")
)

func charValue(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'A' && c <= 'Z':
		i := strings.IndexByte(letters, c)
		if i < 0 {
			return 0, false // I, O, Q
		}
		return letterValues[i], true
	}
	return 0, false
}

// compute returns the expected check character for a VIN whose position 9 is ignored.
func compute(v string) (byte, error) {
	total := 0
	for i := 0; i < Length; i++ {
		val, ok := charValue(v[i])
		if !ok {
			return 0, ErrCharacter
		}
		total += val * weights[i]
	}
	if r := total % 11; r != 10 {
		return byte('0' + r), nil
	}
	return 'X', nil
}

// Check validates length, alphabet and check digit.
func Check(v string) error {
	if len(v) != Length {
		return ErrLength
	}
	want, err := compute(v)
	if err != nil {
		return err
	}
	if v[checkPosition] != want {
		return ErrCheckDigit
	}
	return nil
}

// Valid reports whether v is a well-formed VIN with a correct check digit.
func Valid(v string) bool { return Check(v) == nil }

// Build fills the check digit of a 17-character template whose position 9 is 'x'
// (any other character there is replaced). The remaining characters must be in the VIN alphabet.
func Build(template string) (string, error) {
	if len(template) != Length {
		return "", ErrBadTemplate
	}
	b := []byte(strings.ToUpper(template))
	b[checkPosition] = '0'
	cd, err := compute(string(b))
	if err != nil {
		return "", err
	}
	b[checkPosition] = cd
	return string(b), nil
}

// Alphabet returns the characters a VIN body may use (for generators).
func Alphabet() string { return alphabet }
