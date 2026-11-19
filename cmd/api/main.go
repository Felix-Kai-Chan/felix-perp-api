package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chantrumps/felix-perp-api/pkg/api"

	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/book"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/config"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/matcher"
	"github.com/chantrumps/felix-perp-core/felix-perp-orderbook/pkg/store"

	"github.com/joho/godotenv"
)

func main() {
	// config.yaml 在 orderbook 仓库；本进程作为顶层消费者直接引用它。
	configPath := flag.String("config", "../felix-perp-orderbook/config.yaml", "path to config.yaml")
	flag.Parse()

	_ = godotenv.Load()

	log.Println(" FelixPerp API gateway starting...")

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf(" load config: %v", err)
	}

	ob := book.NewOrderbook()
	m := matcher.NewMatcher(ob)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.New(ctx, cfg, ob)
	if err != nil {
		log.Fatalf(" init store: %v", err)
	}
	defer st.Close()

	m.SetStore(st) // Match 完成自动落 fills + 更新持仓

	if err := st.RebuildBook(ctx); err != nil {
		log.Fatalf(" rebuild book: %v", err)
	}

	go st.Run(ctx) // 快照 ticker + outbox worker

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = cfg.ListenAddr
	}
	if addr == "" {
		addr = ":8080"
	}

	apiSrv := api.New(ob, m, st)
	mux := http.NewServeMux()
	apiSrv.RegisterMux(mux)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf(" HTTP API listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf(" http server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
	case <-sigCh:
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	log.Println(" API gateway stopped")
}
