package main

import (
	"context"
	"fmt"
)

type Command struct {
	CustomerID string
	Text       string
}

type Storage interface {
	GetState(ctx context.Context, customerID string) (string, error)
	UpdateState(ctx context.Context, customerID string, newState string) error
}

// 1. DEFINE THE PORT
type Broadcaster interface {
	Broadcast(msg string)
}

type Engine struct {
	store Storage
	bcast Broadcaster // 2. INJECT THE ADAPTER
}

func (e *Engine) Process(ctx context.Context, cmd Command) (string, error) {
	state, err := e.store.GetState(ctx, cmd.CustomerID)
	if err != nil {
		return "", err
	}

	var nextState string
	var replyText string

	switch state {
	case "idle":
		if cmd.Text == "/start" || cmd.Text == "order" {
			nextState = "ordering"
			replyText = "Welcome to OmniPOS! What would you like to order?"
		} else {
			nextState = "idle"
			replyText = "Send 'order' to start a new order."
		}
	case "ordering":
		nextState = "awaiting_address"
		replyText = fmt.Sprintf("Got it: %s. Please reply with your delivery address.", cmd.Text)
	case "awaiting_address":
		nextState = "idle"
		replyText = fmt.Sprintf("Address confirmed: %s. We are preparing your order!", cmd.Text)

		e.bcast.Broadcast(fmt.Sprintf("NEW ORDER | Customer: %s | Address: %s", cmd.CustomerID, cmd.Text))
	default:
		nextState = "idle"
		replyText = "System reset. Send 'order' to start."
	}

	if state != nextState {
		if err := e.store.UpdateState(ctx, cmd.CustomerID, nextState); err != nil {
			return "", err
		}
	}

	return replyText, nil
}
