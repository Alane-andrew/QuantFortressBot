// exchange/client.go
package exchange

import (
	"auto_bian_go_1/logs"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ensure APIClient struct implements Client interface
var _ Client = (*APIClient)(nil)

// ExchangeInfo holds the full exchange information response.
type ExchangeInfo struct {
	Symbols []SymbolInfo `json:"symbols"`
}

// APIClient is the client for interacting with Binance API
type APIClient struct {
	ApiKey              string
	ApiSecret           string
	BaseURL             string
	Http                *http.Client
	timeOffset          int64 // Difference between server time and local time
	recvWindow          int64 // New: Request valid window time (milliseconds)
	symbolInfoCache     map[string]SymbolInfo
	symbolInfoMutex     sync.RWMutex
	orderUpdateCallback OrderUpdateCallback
	mu                  sync.Mutex // New: Mutex for protecting concurrent requests
}

// Binance API error response struct
type binanceError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// PositionRisk is used to parse position risk API response
type PositionRisk struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	UnrealizedProfit string `json:"unRealizedProfit"`
	PositionSide     string `json:"positionSide"`
	InitialMargin    string `json:"initialMargin"`
	Notional         string `json:"notional"`
	EntryPrice       string `json:"entryPrice"`
}

// BinanceTimeResponse is used to parse server time API response
type BinanceTimeResponse struct {
	ServerTime int64 `json:"serverTime"`
}

// NewAPIClient creates a new API client instance
func NewAPIClient(apiKey, apiSecret, baseURL string, timeoutSeconds int, recvWindowSeconds int) *APIClient {
	return &APIClient{
		ApiKey:          apiKey,
		ApiSecret:       apiSecret,
		BaseURL:         baseURL,
		Http:            &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		timeOffset:      0,                               // Initially 0, updated after SyncTime
		recvWindow:      int64(recvWindowSeconds * 1000), // Convert to milliseconds
		symbolInfoCache: make(map[string]SymbolInfo),
	}
}

// SetOrderUpdateCallback sets a callback function to receive notifications when order status updates.
// Note: In APIClient (real trading), this callback is triggered by polling logic in Orchestrator, not by the client itself.
// This method is mainly to satisfy the Client interface definition.
func (c *APIClient) SetOrderUpdateCallback(callback OrderUpdateCallback) {
	c.orderUpdateCallback = callback
}

// SyncTime synchronizes time with Binance server and calculates offset.
func (c *APIClient) SyncTime() error {
	resp, err := c.Http.Get(c.BaseURL + "/fapi/v1/time")
	if err != nil {
		return fmt.Errorf("Unable to get Binance server time: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Failed to read time response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Get server time API error: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	var timeResp BinanceTimeResponse
	if err := json.Unmarshal(body, &timeResp); err != nil {
		return fmt.Errorf("Failed to parse server time JSON: %w, body: %s", err, string(body))
	}

	localTime := time.Now().UnixMilli()
	c.timeOffset = timeResp.ServerTime - localTime
	logs.Infof("[API Client] Time synchronization completed, local time vs server time difference: %d ms", c.timeOffset)

	// After time synchronization, immediately fetch and cache exchange information
	if err := c.fetchExchangeInfo(); err != nil {
		// Only log warning here, as even if trading info fetch fails, some basic functions (like price queries) may still need to run
		logs.Warnf("[API Client] Failed to fetch and cache exchange trading rules: %v", err)
	}

	return nil
}

// sendRequest is a generic request sending function, handling signing, sending, and error handling
func (c *APIClient) sendRequest(ctx context.Context, method, endpoint string, params url.Values, target interface{}) error {
	// --- FIX: Thread safety fix ---
	// Lock to ensure only one goroutine can send requests through this client instance at a time.
	// This can prevent race conditions when accessing (e.g., updating timeOffset or using http connection pool).
	c.mu.Lock()
	defer c.mu.Unlock()

	// Use calibrated timestamp
	timestamp := time.Now().UnixMilli() + c.timeOffset
	params.Set("timestamp", strconv.FormatInt(timestamp, 10))
	params.Set("recvWindow", strconv.FormatInt(c.recvWindow, 10))

	// Prepare request
	var req *http.Request
	var err error

	// --- Signature logic correction ---
	// Regardless of the request method (GET, POST, DELETE), the signature should be in the query string
	queryString := params.Encode()
	mac := hmac.New(sha256.New, []byte(c.ApiSecret))
	_, _ = mac.Write([]byte(queryString))
	signature := hex.EncodeToString(mac.Sum(nil))

	fullURL := fmt.Sprintf("%s%s?%s&signature=%s", c.BaseURL, endpoint, queryString, signature)

	// For POST or DELETE, the request body should be empty because all parameters are in the URL
	// (This is a common Binance API usage pattern, but for some specific endpoints requiring complex JSON bodies, it may need adjustment)
	var bodyReader io.Reader
	if method == http.MethodPost || method == http.MethodDelete {
		// According to Binance documentation, for application/x-www-form-urlencoded,
		// even for POST/DELETE, parameters should be used in the query string for signing,
		// and the request body can be empty.
		// But for safety, we still put parameters in the body, which is usually more compatible.
		// Update: The correct approach is that the signature content must be exactly the same as the content sent.
		// If parameters are in the URL, sign in the URL. If they are to be put in the Body, then the Body must also be consistent.
		// The simplest way is to pass all parameters only in the URL.
		// Here we clear bodyReader because everything is already in the URL.
		bodyReader = nil
	}

	req, err = http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err == nil && (method == http.MethodPost || method == http.MethodDelete) {
		// Even if body is empty, some servers may require this header to correctly process the request
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	// --- Correction ends ---

	if err != nil {
		return fmt.Errorf("Failed to create request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.ApiKey)

	// Send request
	resp, err := c.Http.Do(req)
	if err != nil {
		return fmt.Errorf("Failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Failed to read response body: %w", err)
	}

	// Check API error response
	if resp.StatusCode >= 400 {
		var errResp binanceError
		if json.Unmarshal(body, &errResp) == nil {
			return fmt.Errorf("API error: %s (code: %d)", errResp.Msg, errResp.Code)
		}
		return fmt.Errorf("API error: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	// Decode to target struct
	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("Failed to decode JSON: %w, body: %s", err, string(body))
		}
	}

	return nil
}

// SetMarginType sets margin type for a specified trading pair
func (c *APIClient) SetMarginType(symbol, marginType string) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("marginType", marginType)
	return c.sendRequest(context.Background(), http.MethodPost, "/fapi/v1/marginType", params, nil)
}

// PlaceOrder submits a new order to the exchange
func (c *APIClient) PlaceOrder(ctx context.Context, order *Order) (*Order, error) {
	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", string(order.Side))
	params.Set("positionSide", string(order.PositionSide))
	params.Set("type", string(order.Type))

	if order.Type == Limit {
		params.Set("timeInForce", "GTC")
		params.Set("price", order.Price)
	}
	if order.Type == Stop {
		params.Set("stopPrice", order.StopPrice)
		params.Set("price", order.Price) // Price for stop limit order
		params.Set("timeInForce", "GTC")
	}

	params.Set("quantity", order.OrigQty)

	// Binance API has a special rule for dual-position mode:
	// For unambiguous closing orders (e.g., BUY order for short position),
	// reduceOnly=true parameter is not needed and cannot be carried.
	isUnambiguousClose := (order.Side == Buy && order.PositionSide == Short) || (order.Side == Sell && order.PositionSide == Long)
	if order.ReduceOnly && !isUnambiguousClose {
		params.Set("reduceOnly", "true")
	}

	var newOrder Order
	err := c.sendRequest(ctx, http.MethodPost, "/fapi/v1/order", params, &newOrder)
	if err != nil {
		return nil, err
	}
	return &newOrder, nil
}

// GetOrder retrieves order details
func (c *APIClient) GetOrder(ctx context.Context, symbol, orderID string) (*Order, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", orderID)

	var order Order
	err := c.sendRequest(ctx, http.MethodGet, "/fapi/v1/order", params, &order)
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetOpenOrders retrieves all open orders for the account.
func (c *APIClient) GetOpenOrders() ([]Order, error) {
	params := url.Values{}
	// By not setting a symbol, we get all open orders.

	var openOrders []*Order
	err := c.sendRequest(context.Background(), http.MethodGet, "/fapi/v1/openOrders", params, &openOrders)
	if err != nil {
		return nil, err
	}

	// Convert []*Order to []Order to satisfy the interface
	orders := make([]Order, len(openOrders))
	for i, o := range openOrders {
		if o != nil {
			orders[i] = *o
		}
	}
	return orders, nil
}

// CancelOrder cancels an active order
func (c *APIClient) CancelOrder(ctx context.Context, symbol, orderID string) (*Order, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", orderID)

	var canceledOrder Order
	err := c.sendRequest(ctx, http.MethodDelete, "/fapi/v1/order", params, &canceledOrder)
	if err != nil {
		return nil, err
	}
	return &canceledOrder, nil
}

// GetPrice retrieves the latest price for a trading pair
func (c *APIClient) GetPrice(symbol string) (float64, error) {
	params := url.Values{}
	params.Set("symbol", symbol)

	var data struct {
		Price string `json:"price"`
	}

	err := c.sendRequest(context.Background(), http.MethodGet, "/fapi/v1/ticker/price", params, &data)
	if err != nil {
		return 0, err
	}

	return strconv.ParseFloat(data.Price, 64)
}

// GetPositionInfo queries position information for a specified trading pair
func (c *APIClient) GetPositionInfo(ctx context.Context, symbol, positionSide string) (*PositionInfo, error) {
	params := url.Values{}
	params.Set("symbol", symbol)

	var positionRisks []PositionRisk
	err := c.sendRequest(ctx, http.MethodGet, "/fapi/v2/positionRisk", params, &positionRisks)
	if err != nil {
		return nil, err
	}

	var anyPosition *PositionInfo = nil
	upperPositionSide := strings.ToUpper(positionSide)

	for _, p := range positionRisks {
		posAmt, _ := strconv.ParseFloat(p.PositionAmt, 64)

		// Prioritize finding an exact match for position
		if p.PositionSide == upperPositionSide {
			unrealizedPnl, _ := strconv.ParseFloat(p.UnrealizedProfit, 64)
			initialMargin, _ := strconv.ParseFloat(p.InitialMargin, 64)
			notional, _ := strconv.ParseFloat(p.Notional, 64)
			entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)

			return &PositionInfo{
				Symbol:           p.Symbol,
				PositionAmt:      posAmt,
				UnrealizedProfit: unrealizedPnl,
				InitialMargin:    initialMargin,
				Notional:         notional,
				EntryPrice:       entryPrice,
				PositionSide:     p.PositionSide,
			}, nil
		}

		// If no exact match, record the first non-zero position found as a fallback
		if posAmt != 0 && anyPosition == nil {
			unrealizedPnl, _ := strconv.ParseFloat(p.UnrealizedProfit, 64)
			initialMargin, _ := strconv.ParseFloat(p.InitialMargin, 64)
			notional, _ := strconv.ParseFloat(p.Notional, 64)
			entryPrice, _ := strconv.ParseFloat(p.EntryPrice, 64)
			anyPosition = &PositionInfo{
				Symbol:           p.Symbol,
				PositionAmt:      posAmt,
				UnrealizedProfit: unrealizedPnl,
				InitialMargin:    initialMargin,
				Notional:         notional,
				EntryPrice:       entryPrice,
				PositionSide:     p.PositionSide,
			}
		}
	}

	// If exact match fails but a non-zero position is found, return it
	if anyPosition != nil {
		logs.Warnf("[Position Query] Exact direction '%s' not found, but a '%s' position was found. Returning that position info.", positionSide, anyPosition.PositionSide)
		return anyPosition, nil
	}

	// If no positions
	return &PositionInfo{
		Symbol:       symbol,
		PositionSide: upperPositionSide,
	}, nil
}

// CancelAllOpenOrders cancels all open orders for a specified trading pair
func (c *APIClient) CancelAllOpenOrders(ctx context.Context, symbol string) error {
	params := url.Values{}
	params.Set("symbol", symbol)

	// This endpoint returns an empty array or success message on success, and an error object on failure.
	// We do not care about the specific content of the success, so target is nil.
	return c.sendRequest(ctx, http.MethodDelete, "/fapi/v1/allOpenOrders", params, nil)
}

// GetSymbolInfo safely retrieves symbol information from the cache.
func (c *APIClient) GetSymbolInfo(symbol string) (SymbolInfo, bool) {
	c.symbolInfoMutex.RLock()
	defer c.symbolInfoMutex.RUnlock()
	info, ok := c.symbolInfoCache[symbol]
	return info, ok
}

// fetchExchangeInfo retrieves and caches exchange information.
func (c *APIClient) fetchExchangeInfo() error {
	resp, err := c.Http.Get(c.BaseURL + "/fapi/v1/exchangeInfo")
	if err != nil {
		return fmt.Errorf("Unable to get Binance exchange info: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("Failed to read exchange info response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Get exchange info API error: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	var exchangeInfo ExchangeInfo
	if err := json.Unmarshal(body, &exchangeInfo); err != nil {
		return fmt.Errorf("Failed to parse exchange info JSON: %w, body: %s", err, string(body))
	}

	c.symbolInfoMutex.Lock()
	defer c.symbolInfoMutex.Unlock()
	for _, symbolInfo := range exchangeInfo.Symbols {
		c.symbolInfoCache[symbolInfo.Symbol] = symbolInfo
	}
	logs.Infof("[API Client] Binance exchange info cache updated, cached %d symbol info.", len(c.symbolInfoCache))
	return nil
}
