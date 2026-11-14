package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Currency describes how integer minor units are shown to customers. Amounts are always stored
// and added as integers; this type only formats them.
type Currency struct {
	Code     string // ISO 4217 code such as IDR
	Decimals int    // digits after the decimal point; 0 means the stored amount is a whole unit
}

// Format renders a non-negative amount, for example "IDR 25,000" or "USD 19.99".
func (c Currency) Format(minor int64) string {
	divisor := int64(1)
	for i := 0; i < c.Decimals; i++ {
		divisor *= 10
	}

	out := groupThousands(minor / divisor)
	if c.Decimals > 0 {
		out += fmt.Sprintf(".%0*d", c.Decimals, minor%divisor)
	}
	return c.Code + " " + out
}

func groupThousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
