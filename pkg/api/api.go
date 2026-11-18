package api

import (
	"encoding/json"
	"log"
	"math"
	"math/big"
	"net/http"
	"time"

	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/book"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/matcher"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/store"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/types"
	"github.com/ethereum/go-ethereum/common"
)

const scale = 1e8

// toOnChain 把人类可读价格/数量（如 64000.0、1.0）换算成链上 8 位精度整数。
func toOnChain(f float64) *big.Int {
	return big.NewInt(int64(math.Round(f * scale)))
}

// fromOnChain 是 toOnChain 的逆运算。
func fromOnChain(i *big.Int) float64 {
	f, _ := new(big.Float).Quo(
		new(big.Float).SetInt(i),
		new(big.Float).SetInt64(scale),
	).Float64()
	return f
}

// Server 持有 HTTP 层需要的全部依赖：簿、撮合、持久化。
// 由 main 组装好后注入，api 包自身不负责初始化任何底层组件。
type Server struct {
	ob *book.Orderbook
	m  *matcher.Matcher
	st *store.Store
}

// New 构造 HTTP server 的逻辑载体。依赖注入，不做任何 IO 初始化。
func New(ob *book.Orderbook, m *matcher.Matcher, st *store.Store) *Server {
	return &Server{ob: ob, m: m, st: st}
}

// RegisterMux 把所有路由挂到给定 mux 上。main 只需调用一次。
func (s *Server) RegisterMux(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/orders", s.handlePostOrder)
	mux.HandleFunc("GET /api/depth", s.handleGetDepth)
}

// orderReq 是 POST /api/orders 的请求体。
// user 为可选字段：留空时落 0x0 默认账户；传入十六进制地址即可区分 maker/taker，
// 持仓净额模型才会产生非零持仓。
type orderReq struct {
	ClientOrderID string  `json:"client_order_id"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Price         float64 `json:"price"`
	Qty           float64 `json:"qty"`
	User          string  `json:"user"`
}

func (s *Server) handlePostOrder(w http.ResponseWriter, r *http.Request) {
	var req orderReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("📥 Order received: %s %s LIMIT %.6f@%.2f by %s",
		req.Side, req.Symbol, req.Qty, req.Price, req.User)

	side := types.SideBuy
	if req.Side == "SELL" {
		side = types.SideSell
	}

	// 可选 user：传入地址就用，留空回退到 0x0（保持旧行为，纯内存自成交测试仍可跑）。
	userAddr := common.HexToAddress("0x0")
	if req.User != "" {
		userAddr = common.HexToAddress(req.User)
	}

	order := &types.Order{
		ID:        req.ClientOrderID,
		User:      userAddr,
		Nonce:     uint64(time.Now().UnixNano()),
		Side:      side,
		Price:     toOnChain(req.Price),
		Qty:       toOnChain(req.Qty),
		Expiry:    time.Now().Add(1 * time.Hour).Unix(),
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
	}

	// 在撮合前记录原始数量：ConsumeAndRest 会把 order.Qty 改写成剩余量，
	// 这里先存一份用于回填 status。
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
			fo = append(fo, fillOut{
				Price: fromOnChain(f.Price),
				Qty:   fromOnChain(f.Qty),
			})
		}
		resp["fills"] = fo
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleGetDepth(w http.ResponseWriter, r *http.Request) {
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
