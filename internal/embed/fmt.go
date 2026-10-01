package embed

import "strconv"

func strconvFormat(x float32) string { return strconv.FormatFloat(float64(x), 'f', 6, 32) }
