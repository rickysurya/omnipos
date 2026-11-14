package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestCurrencyFormat(t *testing.T) {
	cases := []struct {
		cur   Currency
		minor int64
		want  string
	}{
		{Currency{"IDR", 0}, 0, "IDR 0"},
		{Currency{"IDR", 0}, 999, "IDR 999"},
		{Currency{"IDR", 0}, 1000, "IDR 1,000"},
		{Currency{"IDR", 0}, 25000, "IDR 25,000"},
		{Currency{"IDR", 0}, 1234567, "IDR 1,234,567"},
		{Currency{"USD", 2}, 5, "USD 0.05"},
		{Currency{"USD", 2}, 1999, "USD 19.99"},
		{Currency{"USD", 2}, 100000, "USD 1,000.00"},
		{Currency{"KWD", 3}, 1500, "KWD 1.500"},
	}
	for _, c := range cases {
		if got := c.cur.Format(c.minor); got != c.want {
			t.Errorf("%+v.Format(%d) = %q, want %q", c.cur, c.minor, got, c.want)
		}
	}
}

func TestTotalOf(t *testing.T) {
	lines := []OrderLine{
		{Name: "a", Quantity: 2, UnitPriceCents: 25000},
		{Name: "b", Quantity: 1, UnitPriceCents: 5000},
		{Name: "c", Quantity: 99, UnitPriceCents: 0},
	}
	if got := totalOf(lines); got != 55000 {
		t.Fatalf("totalOf = %d, want 55000", got)
	}
	if got := totalOf(nil); got != 0 {
		t.Fatalf("totalOf(nil) = %d, want 0", got)
	}
}

func TestLargeMenuFitsInOneTelegramMessage(t *testing.T) {
	menu := make([]MenuItem, 500)
	for i := range menu {
		menu[i] = MenuItem{ID: i + 1, Name: fmt.Sprintf("Menu item number %03d with a fairly long name", i), PriceCents: 12345}
	}

	text := formatMenu(menu, testCurrency)

	if len(text) >= 4096 {
		t.Fatalf("menu is %d bytes, Telegram rejects messages over 4096 characters", len(text))
	}
	if !strings.Contains(text, "more items") {
		t.Fatal("a truncated menu must say that items were left out")
	}
	if !strings.HasPrefix(text, "Menu:\n- Menu item number 000") {
		t.Fatalf("unexpected start: %q", text[:60])
	}
}

func TestSmallMenuIsNotTruncated(t *testing.T) {
	text := formatMenu([]MenuItem{{1, "Nasi Goreng", 25000}}, testCurrency)
	if text != "Menu:\n- Nasi Goreng: IDR 25,000" {
		t.Fatalf("menu = %q", text)
	}
}
