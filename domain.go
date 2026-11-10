package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Command struct {
	CustomerID string
	Text       string
}

type Storage interface {
	GetState(ctx context.Context, customerID string) (string, error)
	UpdateState(ctx context.Context, customerID string, newState string) error
	AddItemToOrder(ctx context.Context, customerID string, item string, quantity int) error
	CompleteOrder(ctx context.Context, customerID string, address string) ([]string, error)
	GetPendingItems(ctx context.Context, customerID string) ([]string, error)
	CancelPendingOrder(ctx context.Context, customerID string) error
}

type Broadcaster interface {
	Broadcast(msg string)
}

type Engine struct {
	store Storage
	bcast Broadcaster
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

func (e *Engine) Process(ctx context.Context, cmd Command) (string, error) {
	text := strings.TrimSpace(cmd.Text)
	lowerText := strings.ToLower(text)

	// Global command interceptor
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
		if lowerText == "/start" || lowerText == "order" {
			nextState = "ordering"
			replyText = "Welcome! Send your order items one by one (e.g., '2x Nasi Goreng'). Type 'done' when finished or '/cancel' to abort."
		} else {
			replyText = "Send 'order' to start a new order."
		}

	case "ordering":
		if lowerText == "/start" || lowerText == "order" {
			replyText = "You are already building an order. Send your next item, type 'done' to checkout, or '/cancel' to stop."
		} else if lowerText == "done" {
			items, err := e.store.GetPendingItems(ctx, cmd.CustomerID)
			if err != nil || len(items) == 0 {
				nextState = "idle"
				_ = e.store.CancelPendingOrder(ctx, cmd.CustomerID)
				replyText = "Your cart is empty. Order cancelled."
			} else {
				nextState = "confirming"
				replyText = fmt.Sprintf("Here is your order summary:\n- %s\n\nIs this correct? (yes/no)", strings.Join(items, "\n- "))
			}
		} else if strings.HasPrefix(text, "/") {
			replyText = "Unrecognized command. Please send menu items, type 'done' to review, or '/cancel' to reset."
		} else {
			itemName, qty := parseItemAndQty(text)
			if err := e.store.AddItemToOrder(ctx, cmd.CustomerID, itemName, qty); err != nil {
				return "", fmt.Errorf("failed to add item: %v", err)
			}
			replyText = fmt.Sprintf("Added: %dx %s. Send another item, or type 'done' to review.", qty, itemName)
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
		} else {
			nextState = "idle"
			items, err := e.store.CompleteOrder(ctx, cmd.CustomerID, text)
			if err != nil {
				return "", fmt.Errorf("failed to complete order: %v", err)
			}

			replyText = fmt.Sprintf("Address confirmed: %s\n\nYour order is being prepared:\n- %s", text, strings.Join(items, "\n- "))

			kdsMsg := fmt.Sprintf("NEW ORDER | Customer: %s | Address: %s | Items: [%s]",
				cmd.CustomerID, text, strings.Join(items, ", "))
			e.bcast.Broadcast(kdsMsg)
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
