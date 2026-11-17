package main

import (
	"encoding/json"
	"log"
	"math"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/book"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/matcher"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/joho/godotenv"
)

const scale = 1e8

func toOnChain(f float64) *big.Int {
	return big.NewInt(int64(math.Round(f * scale)))
}

func fromOnChain(i *big.Int) float64 {
	f, _ := new(big.Float).Quo(
		new(big.Float).SetInt(i),
		new(big.Float).SetInt64(scale),
	).Float64()
	return f
}

func main() {
	_ = godotenv.Load()

	ob := book.NewOrderbook()
	m := matcher.NewMatcher(ob)

	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/orders", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ClientOrderID string  `json:"client_order_id"`
			Symbol        string  `json:"symbol"`
			Side          string  `json:"side"`
			Price         float64 `json:"price"`
			Qty           float64 `json:"qty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		log.Printf("📥 Order received: %s %s LIMIT %.6f@%.2f",
			req.Side, req.Symbol, req.Qty, req.Price)

		side := types.SideBuy
		if req.Side == "SELL" {
			side = types.SideSell
		}

		order := &types.Order{
			ID:        req.ClientOrderID,
			User:      common.HexToAddress("0x0"),
			Nonce:     uint64(time.Now().UnixNano()),
			Side:      side,
			Price:     toOnChain(req.Price),
			Qty:       toOnChain(req.Qty),
			Expiry:    time.Now().Add(1 * time.Hour).Unix(),
			Status:    types.StatusOpen,
			CreatedAt: time.Now(),
		}

		origQty := new(big.Int).Set(order.Qty)

		fills, err := m.Match(order)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		status := "OPEN"
		if len(fills) > 0 {
			filledQty := big.NewInt(0)
			for _, f := range fills {
				filledQty.Add(filledQty, f.Qty)
			}
			if filledQty.Cmp(origQty) >= 0 {
				status = "FILLED"
			} else {
				status = "PARTIALLY_FILLED"
			}
		}

		resp := map[string]any{
			"client_order_id": req.ClientOrderID,
			"status":          status,
		}
		if len(fills) > 0 {
			type fillOut struct {
				Price float64 `json:"price"`
				Qty   float64 `json:"qty"`
			}
			fo := make([]fillOut, 0, len(fills))
			for _, f := range fills {
				fo = append(fo, fillOut{
					Price: fromOnChain(f.Price),
					Qty:   fromOnChain(f.Qty),
				})
			}
			resp["fills"] = fo
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("GET /api/depth", func(w http.ResponseWriter, r *http.Request) {
		bids, asks := ob.GetDepth(20)
		type levelOut struct {
			Price float64 `json:"price"`
			Qty   float64 `json:"qty"`
		}
		bo := make([]levelOut, 0, len(bids))
		for _, b := range bids {
			bo = append(bo, levelOut{Price: fromOnChain(b.Price), Qty: fromOnChain(b.Qty)})
		}
		ao := make([]levelOut, 0, len(asks))
		for _, a := range asks {
			ao = append(ao, levelOut{Price: fromOnChain(a.Price), Qty: fromOnChain(a.Qty)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"symbol": "BTC-PERP",
			"bids":   bo,
			"asks":   ao,
		})
	})

	server := &http.Server{Addr: ":8080", Handler: mux}

	go func() {
		log.Println("🚀 API listening on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("🛑 API stopped")
}
