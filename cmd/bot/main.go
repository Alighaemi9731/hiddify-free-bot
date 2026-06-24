// Command bot runs the Hiddify free-config Telegram bot.
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Alighaemi9731/hiddify-free-bot/internal/bot"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/config"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/db"
	"github.com/Alighaemi9731/hiddify-free-bot/internal/scheduler"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	store, err := db.Open(cfg.DBPath())
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	b, err := bot.New(cfg, store, version)
	if err != nil {
		log.Fatalf("init bot: %v", err)
	}

	cron := scheduler.Start(b.TB(), store, cfg)
	defer cron.Stop()

	// Graceful shutdown.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("shutting down...")
		b.Stop()
	}()

	log.Printf("hidybot %s starting (admin=%d)", version, cfg.AdminID)
	b.Start()
}
