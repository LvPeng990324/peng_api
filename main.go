package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pengapi/internal/admin"
	"pengapi/internal/auth"
	"pengapi/internal/config"
	"pengapi/internal/relay"
	"pengapi/internal/relay/provider"
	"pengapi/internal/store"
	"pengapi/web"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	logCleanupInterval = 24 * time.Hour
	logCleanupTimeout  = 30 * time.Second
	shutdownTimeout    = 10 * time.Second
	readHeaderTimeout  = 30 * time.Second
	sessionTTL         = 7 * 24 * time.Hour
)

func buildHandler(st *store.Store, engine *relay.Engine, adminH *admin.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // 请求路径 panic 兜底 → 500
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(st))
		r.Post("/chat/completions", engine.ChatCompletions)
		r.Get("/models", engine.Models)
	})
	adminH.RegisterRoutes(r)
	web.RegisterRoutes(r)
	return r
}

// startLogCleaner 启动时清理一次，之后按周期清理，进程退出时随 ctx 停止。
func startLogCleaner(ctx context.Context, st *store.Store, retentionDays int) {
	clean := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
		cctx, cancel := context.WithTimeout(context.Background(), logCleanupTimeout)
		defer cancel()
		if n, err := st.DeleteLogsBefore(cctx, cutoff); err != nil {
			log.Printf("log cleanup: %v", err)
		} else if n > 0 {
			log.Printf("log cleanup: deleted %d rows", n)
		}
	}
	clean()
	go func() {
		ticker := time.NewTicker(logCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				clean()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		log.Fatal("config: ", err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal("store: ", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startLogCleaner(ctx, st, cfg.LogRetentionDays)

	// 上游 http.Client：连接超时走 Transport；整体/首字节超时由 provider 控制
	// 不设 Client.Timeout（会杀死流式响应）
	upstreamClient := &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
	}}
	providers := provider.NewRegistry(
		provider.NewOpenAI(upstreamClient, cfg.RequestTimeout, cfg.StreamFirstByteTimeout),
	)
	engine := relay.NewEngine(st, providers)
	sessions := auth.NewSessionStore(sessionTTL)
	adminH := admin.New(st, sessions, cfg.AdminPassword, providers)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           buildHandler(st, engine, adminH),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	go func() {
		log.Printf("peng_api listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Println("shutting down...")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	srv.Shutdown(sctx)
}
