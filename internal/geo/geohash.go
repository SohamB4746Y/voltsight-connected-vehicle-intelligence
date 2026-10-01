// Package geo provides geohash encoding/decoding and great-circle distance.
package geo

import (
	"errors"
	"math"
	"strings"
)

const base32 = "0123456789bcdefghjkmnpqrstuvwxyz"

var decodeMap = func() [256]int8 {
	var m [256]int8
	for i := range m {
		m[i] = -1
	}
	for i := 0; i < len(base32); i++ {
		m[base32[i]] = int8(i)
	}
	return m
}()

// ErrInvalidGeohash is returned for characters outside the geohash alphabet or empty input.
var ErrInvalidGeohash = errors.New("geo: invalid geohash")

// Encode returns the geohash of (lat, lon) with the given precision (1..12 characters).
func Encode(lat, lon float64, precision int) string {
	if precision < 1 {
		precision = 1
	}
	if precision > 12 {
		precision = 12
	}
	latLo, latHi := -90.0, 90.0
	lonLo, lonHi := -180.0, 180.0
	var sb strings.Builder
	sb.Grow(precision)
	bits, bit, ch := 0, 0, 0
	even := true
	for sb.Len() < precision {
		if even {
			mid := (lonLo + lonHi) / 2
			if lon >= mid {
				ch = ch<<1 | 1
				lonLo = mid
			} else {
				ch <<= 1
				lonHi = mid
			}
		} else {
			mid := (latLo + latHi) / 2
			if lat >= mid {
				ch = ch<<1 | 1
				latLo = mid
			} else {
				ch <<= 1
				latHi = mid
			}
		}
		even = !even
		bit++
		bits++
		if bit == 5 {
			sb.WriteByte(base32[ch])
			bit, ch = 0, 0
		}
	}
	return sb.String()
}

// Bounds returns the bounding box (latLo, latHi, lonLo, lonHi) of a geohash cell.
func Bounds(hash string) (latLo, latHi, lonLo, lonHi float64, err error) {
	if hash == "" {
		return 0, 0, 0, 0, ErrInvalidGeohash
	}
	latLo, latHi, lonLo, lonHi = -90, 90, -180, 180
	even := true
	for i := 0; i < len(hash); i++ {
		v := decodeMap[hash[i]]
		if v < 0 {
			return 0, 0, 0, 0, ErrInvalidGeohash
		}
		for b := 4; b >= 0; b-- {
			set := (v>>uint(b))&1 == 1
			if even {
				mid := (lonLo + lonHi) / 2
				if set {
					lonLo = mid
				} else {
					lonHi = mid
				}
			} else {
				mid := (latLo + latHi) / 2
				if set {
					latLo = mid
				} else {
					latHi = mid
				}
			}
			even = !even
		}
	}
	return latLo, latHi, lonLo, lonHi, nil
}

// Decode returns the centre of the geohash cell.
func Decode(hash string) (lat, lon float64, err error) {
	latLo, latHi, lonLo, lonHi, err := Bounds(hash)
	if err != nil {
		return 0, 0, err
	}
	return (latLo + latHi) / 2, (lonLo + lonHi) / 2, nil
}

const earthRadiusKm = 6371.0088

// HaversineKm is the great-circle distance in kilometres.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// Neighbors returns the (up to) 8 cells adjacent to hash at the same precision. It encodes the points one cell
// width/height away from the cell centre, so it needs no lookup tables and is correct across cell boundaries.
func Neighbors(hash string) []string {
	latLo, latHi, lonLo, lonHi, err := Bounds(hash)
	if err != nil {
		return nil
	}
	lat, lon := (latLo+latHi)/2, (lonLo+lonHi)/2
	dLat, dLon := latHi-latLo, lonHi-lonLo
	var out []string
	for _, di := range []float64{-1, 0, 1} {
		for _, dj := range []float64{-1, 0, 1} {
			if di == 0 && dj == 0 {
				continue
			}
			la, lo := lat+di*dLat, lon+dj*dLon
			if la < -90 || la > 90 {
				continue
			}
			if lo > 180 {
				lo -= 360
			} else if lo < -180 {
				lo += 360
			}
			if h := Encode(la, lo, len(hash)); h != hash {
				out = append(out, h)
			}
		}
	}
	return out
}
