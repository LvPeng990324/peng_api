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

func buildHandler(cfg config.Config, st *store.Store, engine *relay.Engine, adminH *admin.Handler) http.Handler {
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

	// 日志清理：启动时一次 + 每 24h
	cleanLogs := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -cfg.LogRetentionDays)
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if n, err := st.DeleteLogsBefore(cctx, cutoff); err != nil {
			log.Printf("log cleanup: %v", err)
		} else if n > 0 {
			log.Printf("log cleanup: deleted %d rows", n)
		}
	}
	cleanLogs()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanLogs()
			case <-ctx.Done():
				return
			}
		}
	}()

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
	engine := relay.NewEngine(st, providers, cfg.FailThreshold)
	sessions := auth.NewSessionStore(7 * 24 * time.Hour)
	adminH := admin.New(st, sessions, cfg.AdminPassword, providers)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           buildHandler(cfg, st, engine, adminH),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		log.Printf("peng_api listening on %s (db: %s)", cfg.Addr, cfg.DBPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Println("shutting down...")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
}
