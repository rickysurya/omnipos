package main

// All SQL lives here, so postgres.go reads as plain control flow and every statement can be run
// against a real PostgreSQL server on its own.
const (
	queryEnsureCustomer = `
		INSERT INTO customers (chat_id, current_state)
		VALUES ($1, 'idle')
		ON CONFLICT (chat_id) DO NOTHING`

	queryGetState = `SELECT current_state FROM customers WHERE chat_id = $1`

	queryUpdateState = `UPDATE customers SET current_state = $1 WHERE chat_id = $2`

	queryListMenu = `
		SELECT id, name, price_cents
		FROM menu_items
		WHERE is_available
		ORDER BY lower(name), id`

	// The unique index orders_one_pending_per_customer makes this a no-op when a cart already exists,
	// even if two messages from the same customer arrive at the same moment. Run it only after
	// queryPendingOrderID found no cart, because it uses up an order number either way.
	queryEnsurePendingOrder = `
		INSERT INTO orders (customer_id, status)
		VALUES ($1, 'pending')
		ON CONFLICT (customer_id) WHERE status = 'pending' DO NOTHING`

	queryPendingOrderID = `SELECT id FROM orders WHERE customer_id = $1 AND status = 'pending'`

	// Name and price are copied from the menu row in the same statement that checks availability,
	// so an item that sold out a moment ago inserts nothing and the price can never be stale.
	queryAddItem = `
		INSERT INTO order_items (order_id, menu_item_id, item_name, quantity, unit_price_cents)
		SELECT $1::int, id, name, $3::int, price_cents
		FROM menu_items
		WHERE id = $2::int AND is_available`

	queryOrderLines = `
		SELECT item_name, quantity, unit_price_cents
		FROM order_items
		WHERE order_id = $1
		ORDER BY id`

	queryCompleteOrder = `
		UPDATE orders
		SET status = 'confirmed',
		    address = $2,
		    total_cents = (
		        SELECT COALESCE(SUM(quantity * unit_price_cents), 0)::bigint
		        FROM order_items
		        WHERE order_id = orders.id
		    )
		WHERE customer_id = $1 AND status = 'pending'
		RETURNING id, total_cents`

	queryCancelPending = `
		UPDATE orders
		SET status = 'cancelled'
		WHERE customer_id = $1 AND status = 'pending'`

	// The status guard makes a kitchen update atomic: it applies only to an order in the expected step.
	queryUpdateOrderStatus = `
		UPDATE orders
		SET status = $3
		WHERE id = $1 AND status = $2
		RETURNING customer_id`

	queryUpdateSeen = `SELECT EXISTS (SELECT 1 FROM processed_updates WHERE update_id = $1)`

	queryRecordUpdate = `
		INSERT INTO processed_updates (update_id)
		VALUES ($1)
		ON CONFLICT DO NOTHING`
)
