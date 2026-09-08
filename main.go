package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"dbbackup/backup"
	"dbbackup/config"

	"github.com/robfig/cron/v3"
)

func main() {
	runNow := flag.Bool("now", false, "Run the backup process immediately and exit")
	configPath := flag.String("config", "config.yaml", "Path to the config file")
	flag.Parse()

	cfg, err := config.ReadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *runNow {
		log.Println("Running backup immediately...")
		if err := backup.PerformBackup(ctx, cfg); err != nil {
			log.Printf("Backup failed: %v", err)
			os.Exit(1)
		}
		return
	}

	c := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	_, err = c.AddFunc(cfg.Cron, func() {
		if err := backup.PerformBackup(ctx, cfg); err != nil {
			log.Printf("Backup failed: %v", err)
		}
	})
	if err != nil {
		log.Fatalf("Failed to schedule backup job: %v", err)
	}

	log.Printf("Backup scheduler started with cron: %s", cfg.Cron)
	c.Start()

	<-ctx.Done()
	log.Println("Shutdown signal received, aborting any running backup...")
	<-c.Stop().Done()
	log.Println("Scheduler stopped")
}
