package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
)

type Order struct {
	ClientOrderID string  `json:"client_order_id"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Type          string  `json:"type"`
	Price         float64 `json:"price"`
	Qty           float64 `json:"qty"`
}

type DepthLevel struct {
	Price float64 `json:"price"`
	Qty   float64 `json:"qty"`
}

type DepthResponse struct {
	Symbol string       `json:"symbol"`
	Bids   []DepthLevel `json:"bids"`
	Asks   []DepthLevel `json:"asks"`
}

var (
	ordersMu sync.RWMutex
	orders   []Order
)

func handleOrders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	var o Order
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}

	if o.Symbol == "" || o.Side == "" || o.Qty <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid order"})
		return
	}

	ordersMu.Lock()
	orders = append(orders, o)
	ordersMu.Unlock()

	log.Printf("📥 Order received: %s %s %s %.6f@%.2f", o.Side, o.Symbol, o.Type, o.Qty, o.Price)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":          "accepted",
		"client_order_id": o.ClientOrderID,
	})
}

func handleDepth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		symbol = "BTC-PERP"
	}

	ordersMu.RLock()
	defer ordersMu.RUnlock()

	var bids []DepthLevel
	var asks []DepthLevel

	for _, o := range orders {
		if o.Symbol != symbol {
			continue
		}
		if o.Side == "BUY" {
			bids = append(bids, DepthLevel{Price: o.Price, Qty: o.Qty})
		} else if o.Side == "SELL" {
			asks = append(asks, DepthLevel{Price: o.Price, Qty: o.Qty})
		}
	}

	resp := DepthResponse{
		Symbol: symbol,
		Bids:   bids,
		Asks:   asks,
	}

	json.NewEncoder(w).Encode(resp)
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/orders", handleOrders)
	mux.HandleFunc("/api/depth", handleDepth)

	addr := ":8080"
	log.Printf("🚀 API listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
