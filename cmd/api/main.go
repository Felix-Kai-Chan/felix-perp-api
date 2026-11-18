package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/chantrumps/felix-perp-api/pkg/api"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/book"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/config"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/matcher"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/store"
	"github.com/joho/godotenv"
)

func main() {
	// config.yaml 在 orderbook 项目根目录，默认指过去；也可 -config 覆盖。
	configPath := flag.String("config", "../felix-perp-orderbook/config.yaml", "path to config.yaml")
	flag.Parse()

	_ = godotenv.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf(" load config: %v", err)
	}

	// ---- 依赖组装（全部在 main 完成，api 包只做注入后的纯路由）----
	ob := book.NewOrderbook()
	m := matcher.NewMatcher(ob)

	st, err := store.New(ctx, cfg, ob)
	if err != nil {
		log.Fatalf(" init store: %v", err)
	}
	defer st.Close()

	// 注入 matcher：Match 完成后自动落 FILL + taker 剩余量 ADD，并驱动持仓更新。
	m.SetStore(st)

	// 重启恢复：Redis 快照打底 + SQLite 增量回放。
	if err := st.RebuildBook(ctx); err != nil {
		log.Fatalf(" rebuild book: %v", err)
	}
	// 后台：快照 ticker + PG outbox 投递 worker。
	go st.Run(ctx)

	// ---- HTTP 层 ----
	srv := api.New(ob, m, st)
	mux := http.NewServeMux()
	srv.RegisterMux(mux)

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("🚀 API listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("🛑 API stopped")
}
