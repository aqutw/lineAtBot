package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aqutw/lineAtBot/data"
	"github.com/aqutw/lineAtBot/internal/bot"
	"github.com/aqutw/lineAtBot/internal/line"
	"github.com/aqutw/lineAtBot/internal/openrouter"
	"github.com/aqutw/lineAtBot/internal/stock"
	"github.com/aqutw/lineAtBot/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	db, err := stock.NewStore(startCtx, data.Snapshot, os.Getenv("DATABASE_URL"), os.Getenv("DATA_SOURCE_LABEL"))
	cancel()
	if err != nil {
		logger.Error("database startup failed", "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()
	model := openrouter.New(os.Getenv("OPENROUTER_API_KEY"), os.Getenv("OPENROUTER_MODEL"))
	lineClient := line.New(os.Getenv("LINE_CHANNEL_ACCESS_TOKEN"))
	service := &bot.Service{Store: db, Model: model, Now: time.Now, Log: logger}
	server := web.New(ctx, service, lineClient, web.Config{Secret: os.Getenv("LINE_CHANNEL_SECRET"),
		LINEEnabled: os.Getenv("LINE_CHANNEL_SECRET") != "" && lineClient.Token != "", ModelEnabled: model.APIKey != "", DemoToken: os.Getenv("DEMO_API_TOKEN")}, logger)
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}
	httpServer := &http.Server{Addr: ":" + port, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 55 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("lineAtBot listening", "port", port, "demo_data", db.Demo)
	if err = httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server failed")
		stop()
	}
	stop()
	server.Wait()
}
