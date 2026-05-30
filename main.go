package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	rootagent "qqbot-ai/internal/agent"
	"qqbot-ai/internal/capabilities/vision"
	"qqbot-ai/internal/config"
	"qqbot-ai/internal/db"
	"qqbot-ai/internal/llm"
	"qqbot-ai/internal/metric"
	"qqbot-ai/internal/napcat"
	"qqbot-ai/internal/news"
	"qqbot-ai/internal/ops"
	"syscall"
	"time"
)

func main() {
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	store, err := db.OpenStore("data/qqbot-ai-store.sqlite")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	events := rootagent.NewEventQueue()
	llmClient := llm.NewLLMClient(cfg, store)
	napcatGateway := napcat.NewNapcatGateway(cfg, store, events, vision.Agent{Client: llmClient})
	agentRuntime := rootagent.NewAgentRuntime(cfg, store, events, llmClient, napcatGateway)
	metrics := metric.NewMetricService(store)
	charts := metric.NewMetricChartService(store, metrics)
	ithomePoller := news.NewIthomePoller(cfg, store, events)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := napcatGateway.Start(ctx); err != nil {
		store.Log("warn", "NapCat gateway start failed; backend continues", map[string]any{"error": err.Error()})
	}
	ithomePoller.Start(ctx)
	agentRuntime.Start(ctx)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           ops.NewHTTPServer(cfg, store, llmClient, napcatGateway, agentRuntime, charts),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		napcatGateway.Stop()
		store.Log("info", "Server stopped", map[string]any{"event": "server.stopped"})
	}()

	store.Log("info", "Server started", map[string]any{
		"event":          "server.started",
		"port":           cfg.Server.Port,
		"listenGroupIds": cfg.Server.Napcat.ListenGroupIDs,
	})

	err = server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
}
