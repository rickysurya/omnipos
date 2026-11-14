package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStorage struct {
	db *pgxpool.Pool
}

func NewPostgresStorage(db *pgxpool.Pool) *PostgresStorage {
	return &PostgresStorage{db: db}
}

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (p *PostgresStorage) GetState(ctx context.Context, customerID string) (string, error) {
	if _, err := p.db.Exec(ctx, queryEnsureCustomer, customerID); err != nil {
		return "", err
	}

	var state string
	err := p.db.QueryRow(ctx, queryGetState, customerID).Scan(&state)
	return state, err
}

func (p *PostgresStorage) UpdateState(ctx context.Context, customerID string, newState string) error {
	_, err := p.db.Exec(ctx, queryUpdateState, newState, customerID)
	return err
}

func (p *PostgresStorage) ListMenu(ctx context.Context) ([]MenuItem, error) {
	rows, err := p.db.Query(ctx, queryListMenu)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []MenuItem{}
	for rows.Next() {
		var m MenuItem
		if err := rows.Scan(&m.ID, &m.Name, &m.PriceCents); err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

func (p *PostgresStorage) AddItemToOrder(ctx context.Context, customerID string, menuItemID int, quantity int) error {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Look first and insert only when there is no cart. Even an insert that changes nothing uses up
	// a number from the order id sequence, so inserting on every added item would make customers
	// see order numbers jump.
	var orderID int
	err = tx.QueryRow(ctx, queryPendingOrderID, customerID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, queryEnsurePendingOrder, customerID); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, queryPendingOrderID, customerID).Scan(&orderID)
	}
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, queryAddItem, orderID, menuItemID, quantity)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Leaving without a commit also undoes the cart created above, so no empty cart is left behind.
		return ErrItemUnavailable
	}

	return tx.Commit(ctx)
}

func (p *PostgresStorage) GetPendingLines(ctx context.Context, customerID string) ([]OrderLine, error) {
	var orderID int
	err := p.db.QueryRow(ctx, queryPendingOrderID, customerID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return []OrderLine{}, nil
	}
	if err != nil {
		return nil, err
	}
	return scanLines(ctx, p.db, orderID)
}

func (p *PostgresStorage) CompleteOrder(ctx context.Context, customerID string, address string) (PlacedOrder, error) {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return PlacedOrder{}, err
	}
	defer tx.Rollback(ctx)

	var order PlacedOrder
	err = tx.QueryRow(ctx, queryCompleteOrder, customerID, address).Scan(&order.ID, &order.TotalCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlacedOrder{}, ErrNoPendingOrder
	}
	if err != nil {
		return PlacedOrder{}, err
	}

	order.Lines, err = scanLines(ctx, tx, order.ID)
	if err != nil {
		return PlacedOrder{}, err
	}

	return order, tx.Commit(ctx)
}

func (p *PostgresStorage) CancelPendingOrder(ctx context.Context, customerID string) error {
	_, err := p.db.Exec(ctx, queryCancelPending, customerID)
	return err
}

// UpdateOrderStatus moves an order from one status to the next in one atomic statement. The
// status guard in the WHERE clause is what prevents skipped steps and replayed updates.
func (p *PostgresStorage) UpdateOrderStatus(ctx context.Context, orderID int, from, to string) (string, error) {
	var customerID string
	err := p.db.QueryRow(ctx, queryUpdateOrderStatus, orderID, from, to).Scan(&customerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidTransition
	}
	return customerID, err
}

func (p *PostgresStorage) UpdateSeen(ctx context.Context, updateID int64) (bool, error) {
	var seen bool
	err := p.db.QueryRow(ctx, queryUpdateSeen, updateID).Scan(&seen)
	return seen, err
}

func (p *PostgresStorage) RecordUpdate(ctx context.Context, updateID int64) error {
	_, err := p.db.Exec(ctx, queryRecordUpdate, updateID)
	return err
}

func scanLines(ctx context.Context, q querier, orderID int) ([]OrderLine, error) {
	rows, err := q.Query(ctx, queryOrderLines, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []OrderLine{}
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.Name, &l.Quantity, &l.UnitPriceCents); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}
