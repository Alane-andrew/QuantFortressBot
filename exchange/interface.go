package exchange

import (
	"context"
	"time"
)

// SymbolInfo holds information for a single symbol.
type SymbolInfo struct {
	Symbol            string   `json:"symbol"`
	Status            string   `json:"status"`
	PricePrecision    int      `json:"pricePrecision"`
	QuantityPrecision int      `json:"quantityPrecision"`
	Filters           []Filter `json:"filters"`
}

// Filter holds filter data, we use a map to be flexible.
type Filter map[string]interface{}

// OrderSide defines the order direction (BUY or SELL).
type OrderSide string

const (
	Buy  OrderSide = "BUY"
	Sell OrderSide = "SELL"
)

// OrderType defines the order type.
type OrderType string

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
	Stop   OrderType = "STOP"
)

// PositionSide defines the position direction.
type PositionSide string

const (
	Long  PositionSide = "LONG"
	Short PositionSide = "SHORT"
	Both  PositionSide = "BOTH"
)

// OrderStatus defines the order status.
type OrderStatus string

const (
	New             OrderStatus = "NEW"
	PartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	Filled          OrderStatus = "FILLED"
	Canceled        OrderStatus = "CANCELED"
	Expired         OrderStatus = "EXPIRED"
	NewInsurance    OrderStatus = "NEW_INSURANCE"
	NewADL          OrderStatus = "NEW_ADL"
)

// Order represents complete information of an order.
type Order struct {
	Symbol        string       `json:"symbol"`
	OrderID       int64        `json:"orderId"`
	ClientOrderID string       `json:"clientOrderId"`
	Price         string       `json:"price"`
	OrigQty       string       `json:"origQty"`
	ExecutedQty   string       `json:"executedQty"`
	CumQuote      string       `json:"cumQuote"` // Executed quote amount
	Status        OrderStatus  `json:"status"`
	TimeInForce   string       `json:"timeInForce"`
	Type          OrderType    `json:"type"`
	Side          OrderSide    `json:"side"`
	PositionSide  PositionSide `json:"positionSide"`
	StopPrice     string       `json:"stopPrice"` // Only for STOP/TAKE_PROFIT orders
	ReduceOnly    bool         `json:"reduceOnly"`
	ClosePosition bool         `json:"closePosition"` // If true, will close all existing positions
	AvgPrice      string       `json:"avgPrice"`
	OrigType      string       `json:"origType"`
	UpdateTime    int64        `json:"updateTime"`
	orderTime     time.Time    // Only for internal timing in mock client
	ClientID      string       `json:"clientId"` // New: Client-defined order ID for tracking
}

// PositionInfo contains key position information for a single trading pair.
type PositionInfo struct {
	Symbol           string
	PositionAmt      float64
	UnrealizedProfit float64
	InitialMargin    float64 // Initial margin
	Notional         float64 // New: Notional value
	EntryPrice       float64 // Average opening price (added for mock client)
	PositionSide     string  // Position direction (added for mock client)
}

// Client defines the interface that exchange clients need to implement.
type Client interface {
	// SyncTime synchronizes time with the server. This method should be called before making any signed requests.
	SyncTime() error

	// SetMarginType sets the margin mode for the specified trading pair, e.g., "ISOLATED" (isolated) or "CROSSED" (cross).
	SetMarginType(symbol, marginType string) error

	// PlaceOrder submits a new order to the exchange.
	PlaceOrder(ctx context.Context, order *Order) (*Order, error)

	// CancelOrder cancels an active order.
	CancelOrder(ctx context.Context, symbol, orderID string) (*Order, error)

	// GetOrder gets details of an order.
	GetOrder(ctx context.Context, symbol, orderID string) (*Order, error)

	// GetOpenOrders gets all pending orders for this account.
	GetOpenOrders() ([]Order, error)

	// GetPrice gets the latest price for a trading pair.
	GetPrice(symbol string) (float64, error)

	// GetPositionInfo queries position information for the specified trading pair (quantity and unrealized profit/loss).
	GetPositionInfo(ctx context.Context, symbol, positionSide string) (*PositionInfo, error)

	// CancelAllOpenOrders cancels all pending orders for the specified trading pair.
	CancelAllOpenOrders(ctx context.Context, symbol string) error

	// GetSymbolInfo gets trading pair rule information from cache.
	GetSymbolInfo(symbol string) (SymbolInfo, bool)

	// SetOrderUpdateCallback sets a callback function to receive notifications when order status updates (e.g., from mock client).
	// Note: Real API clients may implement this via WebSocket, here provides a unified interface for mock clients.
	SetOrderUpdateCallback(callback OrderUpdateCallback)
}

// OrderUpdateCallback defines the function signature for order update notifications.
// Parameters: order ID, order status, filled price, filled quantity.
type OrderUpdateCallback func(orderID, status string, filledPrice, filledQuantity float64)
