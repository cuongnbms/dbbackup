package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dbbackup/backup"
	"dbbackup/config"
	"dbbackup/notify"

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

	senders := notify.SendersFromEnv()
	if len(cfg.Notify.Events) > 0 && len(senders) == 0 {
		log.Println("Warning: notify.events is configured but neither SLACK_WEBHOOK_URL nor DISCORD_WEBHOOK_URL is set")
	}
	notifier := notify.New(senders, cfg.Notify.Events, os.Getenv("PG_PASSWORD"))

	run := func() error {
		rep, err := backup.PerformBackup(ctx, cfg)
		// A run aborted by SIGTERM leaves ctx cancelled, which would kill the
		// webhook call too — and that run is exactly the one worth reporting.
		nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		for _, ev := range eventsFor(rep, err) {
			notifier.Notify(nctx, messageFor(ev, rep, err))
		}
		return err
	}

	if *runNow {
		log.Println("Running backup immediately...")
		if err := run(); err != nil {
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
		if err := run(); err != nil {
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

// eventsFor maps the outcome of a run onto the events it should report. An
// upload failure is reported as upload_failed even though it is also the
// returned error, because the local artifact is fine and the operator needs to
// know which half broke. That only holds while the upload error *is* the fatal
// error: PerformBackup runs local cleanup after a failed upload, and if that
// cleanup fails it returns the cleanup error with UploadErr still set. Then the
// run is a plain backup_failed, so the fatal error is the one reported.
func eventsFor(rep *backup.Report, err error) []notify.Event {
	if rep == nil {
		return []notify.Event{notify.BackupFailed}
	}
	switch {
	case rep.UploadErr != nil && errors.Is(err, rep.UploadErr):
		return []notify.Event{notify.UploadFailed}
	case err != nil:
		return []notify.Event{notify.BackupFailed}
	}
	events := []notify.Event{notify.BackupSucceeded}
	if rep.RemoteCleanupErr != nil {
		events = append(events, notify.RemoteCleanupFailed)
	}
	return events
}

func messageFor(ev notify.Event, rep *backup.Report, err error) notify.Message {
	if rep == nil {
		rep = &backup.Report{}
	}
	switch ev {
	case notify.UploadFailed:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("❌ db-backup upload failed — %s", rep.Host),
			Body:  fmt.Sprintf("artifact: %s\n%v", rep.Artifact, rep.UploadErr),
		}
	case notify.RemoteCleanupFailed:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("⚠️ db-backup remote cleanup failed — %s", rep.Host),
			Body:  fmt.Sprintf("%v", rep.RemoteCleanupErr),
		}
	case notify.BackupSucceeded:
		return notify.Message{
			Event: ev,
			Title: fmt.Sprintf("✅ db-backup ok — %s", rep.Host),
			Body: fmt.Sprintf("%d databases · %s · %s\n%s",
				len(rep.Databases), humanSize(rep.Size),
				rep.Duration.Round(100*time.Millisecond), rep.Artifact),
		}
	default:
		return notify.Message{
			Event: notify.BackupFailed,
			Title: fmt.Sprintf("❌ db-backup failed — %s", rep.Host),
			Body:  fmt.Sprintf("stage: %s\n%v", rep.Stage, err),
		}
	}
}

// humanSize renders n for a notification body.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
