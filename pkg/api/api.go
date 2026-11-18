package api

import (
	"encoding/json"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/book"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/margin"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/matcher"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/store"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

// scale 与 orderbook/main 口径一致：1 张/1 元 = 1e8。
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

type Server struct {
	ob  *book.Orderbook
	m   *matcher.Matcher
	st  *store.Store
	mux *http.ServeMux
}

func New(ob *book.Orderbook, m *matcher.Matcher, st *store.Store) *Server {
	return &Server{ob: ob, m: m, st: st, mux: http.NewServeMux()}
}

// RegisterMux 把路由挂到调用方传入的 mux 上（main 负责起 http.Server）。
func (s *Server) RegisterMux(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/orders", s.handleOrders)
	mux.HandleFunc("GET /api/depth", s.handleDepth)
	mux.HandleFunc("GET /api/margin", s.handleMargin)
	s.mux = mux
}

// ---------- 下单 ----------

type orderReq struct {
	User          string  `json:"user"`
	ClientOrderID string  `json:"client_order_id"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Price         float64 `json:"price"`
	Qty           float64 `json:"qty"`
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	var req orderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	side := types.SideBuy
	if req.Side == "SELL" {
		side = types.SideSell
	}

	order := &types.Order{
		ID:        req.ClientOrderID,
		User:      common.HexToAddress(req.User),
		Nonce:     uint64(time.Now().UnixNano()),
		Side:      side,
		Price:     toOnChain(req.Price),
		Qty:       toOnChain(req.Qty),
		Expiry:    time.Now().Add(1 * time.Hour).Unix(),
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
	}

	origQty := new(big.Int).Set(order.Qty)

	fills, err := s.m.Match(order)
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
			fo = append(fo, fillOut{Price: fromOnChain(f.Price), Qty: fromOnChain(f.Qty)})
		}
		resp["fills"] = fo
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ---------- 深度 ----------

func (s *Server) handleDepth(w http.ResponseWriter, r *http.Request) {
	bids, asks := s.ob.GetDepth(20)

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
}

// ---------- 保证金 / 强平判定（只读） ----------

type marginItem struct {
	UserAddr          string  `json:"user_addr"`
	Side              string  `json:"side"`
	Qty               float64 `json:"qty"`
	AvgPrice          float64 `json:"avg_price"`
	NotionalValue     string  `json:"notional_value"`
	InitialMargin     string  `json:"initial_margin"`
	UnrealizedPnL     string  `json:"unrealized_pnl"`
	Equity            string  `json:"equity"`
	MaintenanceMargin string  `json:"maintenance_margin"`
	MarginRatio       float64 `json:"margin_ratio"`
	LiquidationPrice  float64 `json:"liquidation_price"`
	CanLiquidate      bool    `json:"can_liquidate"`
}

func (s *Server) handleMargin(w http.ResponseWriter, r *http.Request) {
	markStr := r.URL.Query().Get("mark")
	if markStr == "" {
		http.Error(w, "mark price required (?mark=64000)", http.StatusBadRequest)
		return
	}
	markF, err := strconv.ParseFloat(markStr, 64)
	if err != nil {
		http.Error(w, "invalid mark price", http.StatusBadRequest)
		return
	}
	// 关键：mark 与持仓 AvgPrice 同尺度（1e8），否则 can_liquidate 会错位。
	markPrice := big.NewInt(int64(markF * scale))

	positions := s.st.GetAllPositions()

	items := make([]marginItem, 0, len(positions))
	for _, p := range positions {
		if p.Qty == nil || p.Qty.Sign() == 0 {
			continue
		}
		res := margin.Compute(margin.Position{
			UserAddr: p.User,
			Side:     p.Side.String(),
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
		}, markPrice)

		ratioFloat, _ := strconv.ParseFloat(res.MarginRatio.String(), 64)
		ratioFloat /= float64(margin.WAN) // 万倍基准 -> 倍数（1.0 = 强平线）

		items = append(items, marginItem{
			UserAddr:          p.User,
			Side:              p.Side.String(),
			Qty:               fromOnChain(p.Qty),
			AvgPrice:          fromOnChain(p.AvgPrice),
			NotionalValue:     res.NotionalValue.String(),
			InitialMargin:     res.InitialMargin.String(),
			UnrealizedPnL:     res.UnrealizedPnL.String(),
			Equity:            res.Equity.String(),
			MaintenanceMargin: res.MaintenanceMargin.String(),
			MarginRatio:       ratioFloat,
			LiquidationPrice:  fromOnChain(res.LiquidationPrice),
			CanLiquidate:      res.CanLiquidate,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"mark_price": markStr,
		"positions":  items,
	})
}
