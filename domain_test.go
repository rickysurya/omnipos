package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

var testCurrency = Currency{Code: "IDR", Decimals: 0}

type fakeMenuRow struct {
	MenuItem
	available bool
}

type fakeOrder struct {
	customer string
	status   string
	address  string
	lines    []OrderLine
	total    int64
}

// fakeStore is an in-memory Storage that follows the same rules as the PostgreSQL adapter.
type fakeStore struct {
	states       map[string]string
	menu         []*fakeMenuRow
	orders       []*fakeOrder // the order id is the index plus one
	statusCalls  int
	failGetState bool
	afterList    func() // runs after ListMenu has chosen what to return
}

func defaultMenu() []*fakeMenuRow {
	return []*fakeMenuRow{
		{MenuItem{1, "Nasi Goreng", 25000}, true},
		{MenuItem{2, "Es Teh", 5000}, true},
		{MenuItem{3, "Habis Dish", 10000}, false},
		{MenuItem{4, "7Up", 8000}, true},
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{states: map[string]string{}, menu: defaultMenu()}
}

func (s *fakeStore) pending(customer string) *fakeOrder {
	for _, o := range s.orders {
		if o.customer == customer && o.status == "pending" {
			return o
		}
	}
	return nil
}

func (s *fakeStore) GetState(_ context.Context, customerID string) (string, error) {
	if s.failGetState {
		return "", errors.New("database unavailable")
	}
	if st, ok := s.states[customerID]; ok {
		return st, nil
	}
	s.states[customerID] = "idle"
	return "idle", nil
}

func (s *fakeStore) UpdateState(_ context.Context, customerID, newState string) error {
	s.states[customerID] = newState
	return nil
}

func (s *fakeStore) ListMenu(_ context.Context) ([]MenuItem, error) {
	var items []MenuItem
	for _, row := range s.menu {
		if row.available {
			items = append(items, row.MenuItem)
		}
	}
	if s.afterList != nil {
		s.afterList()
	}
	return items, nil
}

func (s *fakeStore) AddItemToOrder(_ context.Context, customerID string, menuItemID, quantity int) error {
	var found *fakeMenuRow
	for _, row := range s.menu {
		if row.ID == menuItemID && row.available {
			found = row
		}
	}
	if found == nil {
		return ErrItemUnavailable
	}
	o := s.pending(customerID)
	if o == nil {
		o = &fakeOrder{customer: customerID, status: "pending"}
		s.orders = append(s.orders, o)
	}
	o.lines = append(o.lines, OrderLine{Name: found.Name, Quantity: quantity, UnitPriceCents: found.PriceCents})
	return nil
}

func (s *fakeStore) GetPendingLines(_ context.Context, customerID string) ([]OrderLine, error) {
	if o := s.pending(customerID); o != nil {
		return append([]OrderLine{}, o.lines...), nil
	}
	return []OrderLine{}, nil
}

func (s *fakeStore) CompleteOrder(_ context.Context, customerID, address string) (PlacedOrder, error) {
	o := s.pending(customerID)
	if o == nil {
		return PlacedOrder{}, ErrNoPendingOrder
	}
	o.status, o.address, o.total = "confirmed", address, totalOf(o.lines)
	for i, candidate := range s.orders {
		if candidate == o {
			return PlacedOrder{ID: i + 1, Lines: o.lines, TotalCents: o.total}, nil
		}
	}
	return PlacedOrder{}, errors.New("unreachable")
}

func (s *fakeStore) CancelPendingOrder(_ context.Context, customerID string) error {
	if o := s.pending(customerID); o != nil {
		o.status = "cancelled"
	}
	return nil
}

func (s *fakeStore) UpdateOrderStatus(_ context.Context, orderID int, from, to string) (string, error) {
	s.statusCalls++
	if orderID < 1 || orderID > len(s.orders) || s.orders[orderID-1].status != from {
		return "", ErrInvalidTransition
	}
	s.orders[orderID-1].status = to
	return s.orders[orderID-1].customer, nil
}

type fakeBroadcaster struct{ msgs []string }

func (b *fakeBroadcaster) Broadcast(msg string) { b.msgs = append(b.msgs, msg) }

type notification struct{ customerID, text string }

type fakeNotifier struct{ sent []notification }

func (n *fakeNotifier) SendNotification(customerID, message string) error {
	n.sent = append(n.sent, notification{customerID, message})
	return nil
}

type harness struct {
	t     *testing.T
	store *fakeStore
	bc    *fakeBroadcaster
	nt    *fakeNotifier
	eng   *Engine
}

func newHarness(t *testing.T) *harness {
	store, bc, nt := newFakeStore(), &fakeBroadcaster{}, &fakeNotifier{}
	return &harness{t: t, store: store, bc: bc, nt: nt, eng: NewEngine(store, bc, nt, testCurrency)}
}

func (h *harness) say(customer, text string) string {
	h.t.Helper()
	reply, err := h.eng.Process(context.Background(), Command{CustomerID: customer, Text: text})
	if err != nil {
		h.t.Fatalf("Process(%q) returned error: %v", text, err)
	}
	return reply
}

// placeOrder runs one customer through the whole conversation and returns the broadcast KDS message.
func (h *harness) placeOrder(customer, item, address string) string {
	h.t.Helper()
	before := len(h.bc.msgs)
	h.say(customer, "/start")
	h.say(customer, item)
	h.say(customer, "done")
	h.say(customer, "yes")
	h.say(customer, address)
	if len(h.bc.msgs) != before+1 {
		h.t.Fatalf("expected exactly one new KDS broadcast, got %d", len(h.bc.msgs)-before)
	}
	return h.bc.msgs[len(h.bc.msgs)-1]
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("reply %q should contain %q", got, want)
		}
	}
}

func TestOrderFlowIsPricedAndReachesKitchen(t *testing.T) {
	h := newHarness(t)

	mustContain(t, h.say("1", "/start"), "Welcome", "Menu:", "- Nasi Goreng: IDR 25,000", "- Es Teh: IDR 5,000")
	mustContain(t, h.say("1", "2x Nasi Goreng"), "Added: 2x Nasi Goreng (IDR 50,000)")
	mustContain(t, h.say("1", "es teh"), "Added: 1x Es Teh (IDR 5,000)")
	mustContain(t, h.say("1", "done"),
		"- 2x Nasi Goreng (IDR 50,000)\n- 1x Es Teh (IDR 5,000)",
		"Total: IDR 55,000",
		"(yes/no)")
	h.say("1", "yes")
	mustContain(t, h.say("1", "Jl. Melati 5"),
		"Address confirmed: Jl. Melati 5", "Order #1", "- 2x Nasi Goreng (IDR 50,000)", "Total: IDR 55,000")

	want := "NEW_ORDER|ID:1|Customer:1|Address:Jl. Melati 5|Items:2x Nasi Goreng, 1x Es Teh"
	if len(h.bc.msgs) != 1 || h.bc.msgs[0] != want {
		t.Fatalf("KDS messages = %q, want [%q]", h.bc.msgs, want)
	}
	if got := h.store.orders[0]; got.status != "confirmed" || got.total != 55000 {
		t.Fatalf("stored order = %+v, want confirmed with total 55000", got)
	}
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("customer state = %q, want idle", got)
	}
}

func TestCancelFromAnyState(t *testing.T) {
	h := newHarness(t)
	h.say("1", "/start")
	h.say("1", "1x Es Teh")
	h.say("1", "/cancel")

	if got := h.store.orders[0].status; got != "cancelled" {
		t.Fatalf("order status = %q, want cancelled (soft delete)", got)
	}
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("customer state = %q, want idle", got)
	}
}

func TestMenuCommand(t *testing.T) {
	h := newHarness(t)

	idle := h.say("1", "menu")
	mustContain(t, idle, "- Nasi Goreng: IDR 25,000", "- 7Up: IDR 8,000")
	if strings.Contains(idle, "Habis Dish") {
		t.Fatalf("an unavailable item must not be listed: %q", idle)
	}
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("asking for the menu must not start an order, state = %q", got)
	}

	h.say("1", "/start")
	mustContain(t, h.say("1", "/menu"), "Menu:", "Es Teh")
	if got := h.store.states["1"]; got != "ordering" {
		t.Fatalf("state = %q, want ordering to continue", got)
	}
}

func TestEmptyMenuBlocksOrdering(t *testing.T) {
	h := newHarness(t)
	h.store.menu = nil

	mustContain(t, h.say("1", "/start"), "menu is empty")
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("state = %q, want idle when there is nothing to sell", got)
	}
}

func TestItemMatching(t *testing.T) {
	cases := []struct {
		input     string
		wantReply string
	}{
		{"nasi goreng", "Added: 1x Nasi Goreng"},
		{"  NASI    GORENG  ", "Added: 1x Nasi Goreng"},
		{"3 nasi goreng", "Added: 3x Nasi Goreng"},
		{"2X Es Teh", "Added: 2x Es Teh"},
		{"7Up", "Added: 1x 7Up"},
		{"2x 7up", "Added: 2x 7Up"},
		{"pizza", "'pizza' is not on the menu"},
		{"2x pizza", "'2x pizza' is not on the menu"},
		{"Habis Dish", "'Habis Dish' is not on the menu"},
		{"nasi gor", "'nasi gor' is not on the menu"},
		{strings.Repeat("a", 80), "is not on the menu"},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			h := newHarness(t)
			h.say("1", "/start")
			mustContain(t, h.say("1", c.input), c.wantReply)
		})
	}

	t.Run("a long unknown name is shortened in the reply", func(t *testing.T) {
		h := newHarness(t)
		h.say("1", "/start")
		reply := h.say("1", strings.Repeat("a", 500))
		if len(reply) > 200 {
			t.Fatalf("reply echoes the whole input (%d bytes)", len(reply))
		}
	})
}

func TestInvalidInputIsRejected(t *testing.T) {
	cases := []struct {
		name, input, wantReply string
	}{
		{"quantity above the cap", "100x Nasi Goreng", "Quantity must be between 1 and 99"},
		{"huge quantity", "99999999999 Nasi Goreng", "Quantity must be between 1 and 99"},
		{"quantity too large for any integer", "99999999999999999999 Nasi Goreng", "is not on the menu"},
		{"zero quantity", "0x Nasi Goreng", "is not on the menu"},
		{"empty message", "   ", "Please send a text message"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.say("1", "/start")
			mustContain(t, h.say("1", c.input), c.wantReply)
			if len(h.store.orders) != 0 {
				t.Fatalf("rejected input must not create an order, got %d", len(h.store.orders))
			}
		})
	}

	t.Run("quantity at the cap is accepted", func(t *testing.T) {
		h := newHarness(t)
		h.say("1", "/start")
		mustContain(t, h.say("1", fmt.Sprintf("%dx Es Teh", maxQuantity)), "Added: 99x Es Teh (IDR 495,000)")
	})

	t.Run("an order has a maximum number of lines", func(t *testing.T) {
		h := newHarness(t)
		h.say("1", "/start")
		for i := 0; i < maxOrderLines; i++ {
			h.say("1", "Es Teh")
		}
		mustContain(t, h.say("1", "Es Teh"), "at most 25 items")
		if got := len(h.store.orders[0].lines); got != maxOrderLines {
			t.Fatalf("order has %d lines, want %d", got, maxOrderLines)
		}
	})

	t.Run("address too long keeps the customer in the address step", func(t *testing.T) {
		h := newHarness(t)
		h.say("1", "/start")
		h.say("1", "1x Es Teh")
		h.say("1", "done")
		h.say("1", "yes")
		mustContain(t, h.say("1", strings.Repeat("a", maxAddressLen+1)), "limited to 300 characters")
		if got := h.store.states["1"]; got != "awaiting_address" {
			t.Fatalf("state = %q, want awaiting_address", got)
		}
		if len(h.bc.msgs) != 0 {
			t.Fatal("nothing should reach the kitchen")
		}
	})
}

func TestItemSoldOutBetweenMenuAndAdd(t *testing.T) {
	h := newHarness(t)
	h.say("1", "/start")
	h.store.afterList = func() { h.store.menu[0].available = false }

	mustContain(t, h.say("1", "1x Nasi Goreng"), "Nasi Goreng is no longer available")
	if len(h.store.orders) != 0 {
		t.Fatal("a sold out item must not leave a cart behind")
	}
}

func TestPricesAreFixedWhenTheItemIsAdded(t *testing.T) {
	h := newHarness(t)
	h.say("1", "/start")
	h.say("1", "1x Nasi Goreng")
	h.store.menu[0].PriceCents = 99999 // the owner raises the price while the cart is open
	h.say("1", "done")
	h.say("1", "yes")
	mustContain(t, h.say("1", "Jl. A"), "Total: IDR 25,000")
}

func TestEmptyCartIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.say("1", "/start")
	mustContain(t, h.say("1", "done"), "cart is empty")
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("state = %q, want idle", got)
	}
}

func TestCompletingWithoutAnOpenCart(t *testing.T) {
	h := newHarness(t)
	h.store.states["1"] = "awaiting_address"

	mustContain(t, h.say("1", "Jl. A"), "no open order")
	if got := h.store.states["1"]; got != "idle" {
		t.Fatalf("state = %q, want idle", got)
	}
	if len(h.bc.msgs) != 0 {
		t.Fatal("nothing should reach the kitchen")
	}
}

func TestKDSMessageFieldsCannotBeForged(t *testing.T) {
	h := newHarness(t)
	msg := h.placeOrder("1", "1x Nasi Goreng", "Jl. Mawar|Items:100x Free\nCustomer:999")

	if got := strings.Count(msg, "|"); got != 4 {
		t.Fatalf("message has %d field separators, want 4: %q", got, msg)
	}
	if strings.ContainsAny(msg, "\r\n") {
		t.Fatalf("message contains a line break: %q", msg)
	}
	want := "NEW_ORDER|ID:1|Customer:1|Address:Jl. Mawar Items:100x Free Customer:999|Items:1x Nasi Goreng"
	if msg != want {
		t.Fatalf("message = %q, want %q", msg, want)
	}
}

func TestKitchenTransitions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.placeOrder("1", "1x Nasi Goreng", "Jl. A")
	h.placeOrder("2", "1x Es Teh", "Jl. B")

	update := func(orderID int, status string) error {
		return h.eng.ProcessKitchenUpdate(ctx, KDSAction{OrderID: orderID, Status: status})
	}
	notified := func() int { return len(h.nt.sent) }

	if err := update(1, "preparing"); err != nil {
		t.Fatalf("confirmed -> preparing: %v", err)
	}
	last := h.nt.sent[len(h.nt.sent)-1]
	if last.customerID != "1" || !strings.Contains(last.text, "Order #1") || !strings.Contains(last.text, "being prepared") {
		t.Fatalf("unexpected notification: %+v", last)
	}

	rejected := []struct {
		name    string
		orderID int
		status  string
	}{
		{"skipping a step", 2, "ready"},
		{"replaying a step", 1, "preparing"},
		{"going backwards to confirmed", 1, "confirmed"},
		{"cancelling from the kitchen", 1, "cancelled"},
		{"unknown status", 1, "bogus"},
		{"unknown order", 99, "preparing"},
	}
	for _, c := range rejected {
		before, calls := notified(), h.store.statusCalls
		err := update(c.orderID, c.status)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s: err = %v, want ErrInvalidTransition", c.name, err)
		}
		if notified() != before {
			t.Fatalf("%s: a rejected update must not notify the customer", c.name)
		}
		if strings.Contains("confirmed cancelled bogus", c.status) && h.store.statusCalls != calls {
			t.Fatalf("%s: statuses the kitchen may never set must not reach the database", c.name)
		}
	}

	for _, status := range []string{"ready", "completed"} {
		if err := update(1, status); err != nil {
			t.Fatalf("-> %s: %v", status, err)
		}
	}
	if got := h.store.orders[0].status; got != "completed" {
		t.Fatalf("final status = %q, want completed", got)
	}
	if got := h.store.orders[1].status; got != "confirmed" {
		t.Fatalf("order 2 must be untouched, status = %q", got)
	}
}
