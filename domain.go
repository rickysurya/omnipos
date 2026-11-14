package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxAddressLen = 300
	maxQuantity   = 99
	maxOrderLines = 25   // keeps an order summary inside one Telegram message
	maxMenuBytes  = 3500 // Telegram rejects messages longer than 4096 characters

	welcomeText = "Welcome! Send item names from the menu one by one (e.g., '2x Nasi Goreng'). " +
		"Type 'menu' to see it again, 'done' when finished or '/cancel' to abort."
)

var (
	// ErrInvalidTransition is returned when a kitchen update would skip a step or repeat one.
	ErrInvalidTransition = errors.New("invalid order status transition")
	// ErrItemUnavailable is returned when a menu item is missing or sold out at the moment it is added.
	ErrItemUnavailable = errors.New("menu item is not available")
	// ErrNoPendingOrder is returned when a customer has no open cart to complete.
	ErrNoPendingOrder = errors.New("no pending order")
)

// kitchenTransitions lists every status the kitchen may set and the only status each may follow.
var kitchenTransitions = map[string]string{
	"preparing": "confirmed",
	"ready":     "preparing",
	"completed": "ready",
}

type Command struct {
	CustomerID string
	Text       string
}

type KDSAction struct {
	OrderID int    `json:"order_id"`
	Status  string `json:"status"` // "preparing", "ready", "completed"
}

// MenuItem is a sellable item. PriceCents is in the currency's minor units.
type MenuItem struct {
	ID         int
	Name       string
	PriceCents int64
}

// OrderLine is one line of an order as recorded when the item was added.
type OrderLine struct {
	Name           string
	Quantity       int
	UnitPriceCents int64
}

type PlacedOrder struct {
	ID         int
	Lines      []OrderLine
	TotalCents int64
}

type Storage interface {
	GetState(ctx context.Context, customerID string) (string, error)
	UpdateState(ctx context.Context, customerID string, newState string) error
	// ListMenu returns the items that can be ordered right now.
	ListMenu(ctx context.Context) ([]MenuItem, error)
	// AddItemToOrder adds a menu item to the customer's open cart, creating the cart if needed. It
	// records the name and price as they are now, and returns ErrItemUnavailable if the item is gone.
	AddItemToOrder(ctx context.Context, customerID string, menuItemID int, quantity int) error
	GetPendingLines(ctx context.Context, customerID string) ([]OrderLine, error)
	// CompleteOrder confirms the open cart and stores its total. It returns ErrNoPendingOrder when
	// the customer has no open cart.
	CompleteOrder(ctx context.Context, customerID string, address string) (PlacedOrder, error)
	CancelPendingOrder(ctx context.Context, customerID string) error
	// UpdateOrderStatus moves an order from one status to the next in a single atomic step.
	// It returns ErrInvalidTransition when the order is not currently in the from status.
	UpdateOrderStatus(ctx context.Context, orderID int, from, to string) (string, error)
}

type Broadcaster interface {
	Broadcast(msg string)
}

type Notifier interface {
	SendNotification(customerID string, message string) error
}

type Engine struct {
	store    Storage
	bcast    Broadcaster
	notifier Notifier
	currency Currency
}

func NewEngine(store Storage, bcast Broadcaster, notifier Notifier, currency Currency) *Engine {
	return &Engine{store: store, bcast: bcast, notifier: notifier, currency: currency}
}

var itemQtyRegex = regexp.MustCompile(`(?i)^(\d+)\s*x?\s*(.+)$`)

func parseItemAndQty(input string) (string, int) {
	matches := itemQtyRegex.FindStringSubmatch(input)
	if len(matches) == 3 {
		qty, err := strconv.Atoi(matches[1])
		if err == nil && qty > 0 {
			return strings.TrimSpace(matches[2]), qty
		}
	}
	return input, 1
}

func normalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func findMenuItem(menu []MenuItem, name string) (MenuItem, bool) {
	key := normalizeName(name)
	for _, m := range menu {
		if normalizeName(m.Name) == key {
			return m, true
		}
	}
	return MenuItem{}, false
}

// resolveOrder turns a customer message into a menu item and quantity. The whole message is tried
// as an item name first, so an item such as "7Up" is not misread as a quantity of 7.
func resolveOrder(menu []MenuItem, text string) (MenuItem, int, bool) {
	if item, ok := findMenuItem(menu, text); ok {
		return item, 1, true
	}
	name, qty := parseItemAndQty(text)
	if item, ok := findMenuItem(menu, name); ok {
		return item, qty, true
	}
	return MenuItem{}, 0, false
}

func totalOf(lines []OrderLine) int64 {
	var total int64
	for _, l := range lines {
		total += int64(l.Quantity) * l.UnitPriceCents
	}
	return total
}

func formatMenu(menu []MenuItem, cur Currency) string {
	var b strings.Builder
	b.WriteString("Menu:")
	for i, m := range menu {
		line := fmt.Sprintf("\n- %s: %s", m.Name, cur.Format(m.PriceCents))
		if b.Len()+len(line) > maxMenuBytes {
			fmt.Fprintf(&b, "\n...and %d more items. Ask the staff for the full menu.", len(menu)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func formatLines(lines []OrderLine, cur Currency) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("- %dx %s (%s)", l.Quantity, l.Name, cur.Format(int64(l.Quantity)*l.UnitPriceCents))
	}
	return strings.Join(out, "\n")
}

func kitchenItems(lines []OrderLine) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = fmt.Sprintf("%dx %s", l.Quantity, l.Name)
	}
	return strings.Join(out, ", ")
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "..."
}

func isMenuCommand(lowerText string) bool {
	return lowerText == "menu" || lowerText == "/menu"
}

// sanitizeField makes customer supplied text safe to place in the pipe delimited KDS message.
// Without it, an address containing "|" or a newline could forge extra fields for the kitchen.
func sanitizeField(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '|' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (e *Engine) menuReply(ctx context.Context) (string, error) {
	menu, err := e.store.ListMenu(ctx)
	if err != nil {
		return "", err
	}
	if len(menu) == 0 {
		return "The menu is empty right now. Please try again later.", nil
	}
	return formatMenu(menu, e.currency), nil
}

// startOrder opens a new cart conversation and shows the menu, unless there is nothing to sell.
func (e *Engine) startOrder(ctx context.Context) (reply, nextState string, err error) {
	menu, err := e.store.ListMenu(ctx)
	if err != nil {
		return "", "", err
	}
	if len(menu) == 0 {
		return "The menu is empty right now. Please try again later.", "idle", nil
	}
	return welcomeText + "\n\n" + formatMenu(menu, e.currency), "ordering", nil
}

func (e *Engine) addItem(ctx context.Context, customerID, text string) (string, error) {
	menu, err := e.store.ListMenu(ctx)
	if err != nil {
		return "", err
	}

	item, qty, ok := resolveOrder(menu, text)
	if !ok {
		return fmt.Sprintf("'%s' is not on the menu. Send 'menu' to see what is available.", truncateRunes(text, 50)), nil
	}
	if qty > maxQuantity {
		return fmt.Sprintf("Quantity must be between 1 and %d.", maxQuantity), nil
	}

	lines, err := e.store.GetPendingLines(ctx, customerID)
	if err != nil {
		return "", err
	}
	if len(lines) >= maxOrderLines {
		return fmt.Sprintf("An order can have at most %d items. Type 'done' to review it.", maxOrderLines), nil
	}

	if err := e.store.AddItemToOrder(ctx, customerID, item.ID, qty); err != nil {
		if errors.Is(err, ErrItemUnavailable) {
			return fmt.Sprintf("Sorry, %s is no longer available. Send 'menu' to see what is available.", item.Name), nil
		}
		return "", fmt.Errorf("failed to add item: %w", err)
	}

	return fmt.Sprintf("Added: %dx %s (%s). Send another item, or type 'done' to review.",
		qty, item.Name, e.currency.Format(int64(qty)*item.PriceCents)), nil
}

func (e *Engine) Process(ctx context.Context, cmd Command) (string, error) {
	text := strings.TrimSpace(cmd.Text)
	if text == "" {
		return "Please send a text message. Send 'order' to start.", nil
	}
	lowerText := strings.ToLower(text)

	if lowerText == "/cancel" {
		if err := e.store.CancelPendingOrder(ctx, cmd.CustomerID); err != nil {
			return "", err
		}
		if err := e.store.UpdateState(ctx, cmd.CustomerID, "idle"); err != nil {
			return "", err
		}
		return "Order cancelled. Send 'order' to start a new order.", nil
	}

	state, err := e.store.GetState(ctx, cmd.CustomerID)
	if err != nil {
		return "", err
	}

	nextState := state
	var replyText string

	switch state {
	case "idle":
		switch {
		case isMenuCommand(lowerText):
			replyText, err = e.menuReply(ctx)
		case lowerText == "/start" || lowerText == "order":
			replyText, nextState, err = e.startOrder(ctx)
		default:
			replyText = "Send 'order' to start a new order, or 'menu' to see the menu."
		}
		if err != nil {
			return "", err
		}

	case "ordering":
		switch {
		case lowerText == "/start" || lowerText == "order":
			replyText = "You are already building an order. Send your next item, type 'done' to checkout, or '/cancel' to stop."
		case isMenuCommand(lowerText):
			replyText, err = e.menuReply(ctx)
		case lowerText == "done":
			lines, linesErr := e.store.GetPendingLines(ctx, cmd.CustomerID)
			if linesErr != nil {
				return "", linesErr
			}
			if len(lines) == 0 {
				nextState = "idle"
				_ = e.store.CancelPendingOrder(ctx, cmd.CustomerID)
				replyText = "Your cart is empty. Order cancelled."
			} else {
				nextState = "confirming"
				replyText = fmt.Sprintf("Here is your order summary:\n%s\n\nTotal: %s\n\nIs this correct? (yes/no)",
					formatLines(lines, e.currency), e.currency.Format(totalOf(lines)))
			}
		case strings.HasPrefix(text, "/"):
			replyText = "Unrecognized command. Please send menu items, type 'done' to review, or '/cancel' to reset."
		default:
			replyText, err = e.addItem(ctx, cmd.CustomerID, text)
		}
		if err != nil {
			return "", err
		}

	case "confirming":
		if lowerText == "yes" || lowerText == "y" {
			nextState = "awaiting_address"
			replyText = "Order confirmed. Please reply with your delivery address."
		} else if lowerText == "no" || lowerText == "n" {
			nextState = "idle"
			_ = e.store.CancelPendingOrder(ctx, cmd.CustomerID)
			replyText = "Order cancelled. Send 'order' to start a new one."
		} else {
			replyText = "Please reply with 'yes' to confirm or 'no' to cancel."
		}

	case "awaiting_address":
		if strings.HasPrefix(text, "/") {
			replyText = "Please send a valid delivery address or type '/cancel' to abort."
		} else if utf8.RuneCountInString(text) > maxAddressLen {
			replyText = fmt.Sprintf("Addresses are limited to %d characters. Please shorten it.", maxAddressLen)
		} else {
			nextState = "idle"
			order, err := e.store.CompleteOrder(ctx, cmd.CustomerID, text)
			if errors.Is(err, ErrNoPendingOrder) {
				replyText = "You have no open order. Send 'order' to start a new one."
			} else if err != nil {
				return "", fmt.Errorf("failed to complete order: %w", err)
			} else {
				replyText = fmt.Sprintf("Address confirmed: %s\n\nOrder #%d is registered:\n%s\n\nTotal: %s",
					text, order.ID, formatLines(order.Lines, e.currency), e.currency.Format(order.TotalCents))

				kdsMsg := fmt.Sprintf("NEW_ORDER|ID:%d|Customer:%s|Address:%s|Items:%s",
					order.ID,
					sanitizeField(cmd.CustomerID),
					sanitizeField(text),
					sanitizeField(kitchenItems(order.Lines)))
				e.bcast.Broadcast(kdsMsg)
			}
		}

	default:
		nextState = "idle"
		replyText = "Session reset. Send 'order' to start."
	}

	if state != nextState {
		if err := e.store.UpdateState(ctx, cmd.CustomerID, nextState); err != nil {
			return "", err
		}
	}

	return replyText, nil
}

func (e *Engine) ProcessKitchenUpdate(ctx context.Context, action KDSAction) error {
	from, allowed := kitchenTransitions[action.Status]
	if !allowed {
		return fmt.Errorf("%w: kitchen cannot set status %q", ErrInvalidTransition, action.Status)
	}

	customerID, err := e.store.UpdateOrderStatus(ctx, action.OrderID, from, action.Status)
	if err != nil {
		return err
	}

	var msg string
	switch action.Status {
	case "preparing":
		msg = fmt.Sprintf("👨‍🍳 Order #%d is now being prepared in the kitchen!", action.OrderID)
	case "ready":
		msg = fmt.Sprintf("🔔 Order #%d is ready for delivery!", action.OrderID)
	case "completed":
		msg = fmt.Sprintf("✅ Order #%d is complete. Thank you!", action.OrderID)
	}

	if e.notifier != nil {
		return e.notifier.SendNotification(customerID, msg)
	}
	return nil
}
