// Command supervisor is the parent process for the Factorio server. It replaces
// the previous entrypoint.sh + Python scripts: it injects the RCON password,
// synchronises mods, launches the Factorio server (capturing its stdio into an
// in-memory log bus for the web interface), runs a player monitor, serves a
// small HTTP API, and performs a graceful save-and-notify shutdown.
//
// Subcommands:
//
//	supervisor run          launch and supervise the server (default)
//	supervisor sync-mods    assemble /factorio/mods and exit
//	supervisor upload       upload saves to S3 and exit
//	supervisor download     download saves from S3 and exit
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/thomasalbrighton/factorio-supervisor/internal/config"
	"github.com/thomasalbrighton/factorio-supervisor/internal/discord"
	"github.com/thomasalbrighton/factorio-supervisor/internal/factorio"
	"github.com/thomasalbrighton/factorio-supervisor/internal/mods"
	"github.com/thomasalbrighton/factorio-supervisor/internal/monitor"
	"github.com/thomasalbrighton/factorio-supervisor/internal/rcon"
	"github.com/thomasalbrighton/factorio-supervisor/internal/s3"
	"github.com/thomasalbrighton/factorio-supervisor/internal/web"
)

func logf(format string, args ...any) {
	ts := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	fmt.Printf("[%s] [supervisor] %s\n", ts, fmt.Sprintf(format, args...))
}

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	cfg, err := config.Load()
	if err != nil {
		logf("FATAL: config: %v", err)
		os.Exit(1)
	}

	switch cmd {
	case "run":
		os.Exit(runServer(cfg))
	case "sync-mods":
		os.Exit(runSyncMods(cfg))
	case "upload":
		os.Exit(runSaves(cfg, true))
	case "download":
		os.Exit(runSaves(cfg, false))
	default:
		logf("FATAL: unknown subcommand %q (want run|sync-mods|upload|download)", cmd)
		os.Exit(2)
	}
}

// newStore returns an S3 store, or nil if S3 is not configured.
func newStore(cfg *config.Config) *s3.Store {
	if !cfg.S3Configured() {
		return nil
	}
	return s3.New(cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, cfg.AWSEndpointURL, cfg.AWSBucketName)
}

func runSyncMods(cfg *config.Config) int {
	ctx := context.Background()
	m := mods.New(mods.Config{
		ModsDir:    cfg.ModsDir,
		PrivateDir: cfg.PrivateModsDir,
		PublicJSON: cfg.PublicModsJSON,
		Username:   cfg.FactorioUsername,
		Token:      cfg.FactorioToken,
		Store:      newStore(cfg),
		Log:        logf,
	})
	if err := m.Sync(ctx); err != nil {
		logf("FATAL: mod sync failed: %v", err)
		return 1
	}
	return 0
}

func runSaves(cfg *config.Config, upload bool) int {
	store := newStore(cfg)
	if store == nil {
		logf("FATAL: S3 not configured (need AWS_ACCESS_KEY_ID/SECRET/ENDPOINT/BUCKET)")
		return 1
	}
	sm := s3.NewSaveManager(store, cfg.SavesDir, cfg.SaveName, logf)
	ctx := context.Background()
	var err error
	if upload {
		err = sm.Upload(ctx)
	} else {
		err = sm.Download(ctx)
	}
	if err != nil {
		logf("save operation failed: %v", err)
		return 1
	}
	return 0
}

func runServer(cfg *config.Config) int {
	logf("Starting Factorio supervisor: %s", cfg)

	if err := injectRCONPassword(cfg); err != nil {
		logf("WARNING: could not inject RCON password: %v", err)
	}

	notifier := discord.New("")
	if cfg.DiscordConfigured() {
		notifier = discord.New(cfg.DiscordWebhook)
	} else {
		logf("Discord notifications disabled")
	}

	// Startup notification (best-effort).
	if cfg.DiscordConfigured() {
		if err := notifier.ServerStartup(cfg.ServerName); err != nil {
			logf("Failed to send startup notification: %v", err)
		}
	}

	store := newStore(cfg)

	var saveMgr *s3.SaveManager
	if store != nil {
		saveMgr = s3.NewSaveManager(store, cfg.SavesDir, cfg.SaveName, logf)
	}

	// Download the latest save from S3 before launch so the machine boots the
	// newest world, not whatever was baked into the image. A missing save is
	// not fatal (fresh world); a transport error is, to avoid silently starting
	// from a stale local save and then overwriting the good S3 copy on shutdown.
	if saveMgr != nil {
		logf("Downloading latest save from S3...")
		if err := saveMgr.Download(context.Background()); err != nil {
			logf("FATAL: save download failed — aborting to avoid clobbering the S3 save: %v", err)
			return 1
		}
	} else {
		logf("S3 not configured — skipping save download")
	}

	// Synchronise mods before launch; a failure aborts to avoid loading a
	// mismatched mod set (matching the previous entrypoint behaviour).
	modMgr := mods.New(mods.Config{
		ModsDir:    cfg.ModsDir,
		PrivateDir: cfg.PrivateModsDir,
		PublicJSON: cfg.PublicModsJSON,
		Username:   cfg.FactorioUsername,
		Token:      cfg.FactorioToken,
		Store:      store,
		Log:        logf,
	})
	logf("Syncing mods (private + S3-cached public)...")
	if err := modMgr.Sync(context.Background()); err != nil {
		logf("FATAL: Mod sync failed — aborting startup: %v", err)
		return 1
	}

	// Launch Factorio, capturing its stdio into the log bus.
	bus := factorio.NewLogBus(2000)
	proc := factorio.NewProcess(bus, cfg.DockerEntrypoint, os.Args[2:], logf)
	logf("Starting Factorio server...")
	if err := proc.Start(); err != nil {
		logf("FATAL: could not start Factorio: %v", err)
		return 1
	}
	logf("Factorio server started (pid %d)", proc.Pid())

	rconClient := rcon.New(cfg.RCONHost, cfg.RCONPort, cfg.RCONPassword)

	// Background workers share a cancellable context.
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	// Player monitor — start after a short grace period so RCON is up.
	wg.Go(func() {
		select {
		case <-time.After(30 * time.Second):
		case <-workerCtx.Done():
			return
		}
		mon := monitor.New(monitor.Config{
			RCON:         rconClient,
			Notifier:     notifier,
			Saves:        saveMgr,
			ServerName:   cfg.ServerName,
			Interval:     cfg.MonitorInterval,
			NotifyJoin:   cfg.MonitorJoinLeave,
			NotifyStatus: cfg.MonitorStatus,
			Log:          logf,
		})
		mon.Run(workerCtx)
	})

	// Web interface.
	var httpSrv *http.Server
	if cfg.WebEnabled {
		srv := web.New(bus, rconClient, cfg.ServerName, logf)
		httpSrv = &http.Server{Addr: cfg.WebAddr, Handler: srv.Handler()}
		wg.Go(func() {
			logf("Web interface listening on %s", cfg.WebAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logf("web server error: %v", err)
			}
		})
	}

	// Wait for either a shutdown signal or the Factorio process exiting on its
	// own.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	exitCode := 0
	select {
	case sig := <-sigCh:
		logf("Received %s — beginning shutdown sequence", sig)
		exitCode = shutdown(cfg, proc, notifier, saveMgr, cancelWorkers, httpSrv, &wg, true)
	case <-proc.Done():
		werr := proc.Wait()
		if werr != nil {
			logf("Factorio exited with error: %v", werr)
			exitCode = 1
		} else {
			logf("Factorio exited cleanly")
		}
		// Factorio already exited, so don't try to stop it again; still drain
		// workers and upload saves. A save-upload failure also fails the run.
		if shutdown(cfg, proc, notifier, saveMgr, cancelWorkers, httpSrv, &wg, false) != 0 {
			exitCode = 1
		}
	}
	return exitCode
}

// shutdown drains workers, stops Factorio (if still running), uploads saves, and
// notifies Discord. stopFactorio is false when Factorio already exited on its
// own.
func shutdown(
	cfg *config.Config,
	proc *factorio.Process,
	notifier *discord.Notifier,
	saveMgr *s3.SaveManager,
	cancelWorkers context.CancelFunc,
	httpSrv *http.Server,
	wg *sync.WaitGroup,
	stopFactorio bool,
) int {
	start := time.Now()

	// Stop background workers (monitor, web).
	cancelWorkers()
	if httpSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = httpSrv.Shutdown(shutCtx)
		cancel()
	}

	if stopFactorio {
		logf("Sending SIGTERM to Factorio and waiting up to %s for it to save and exit...", cfg.ShutdownGracePeriod)
		took := proc.Stop(cfg.ShutdownGracePeriod)
		logf("Factorio server exited after %s", took.Round(time.Second))
	}

	wg.Wait()

	exitCode := 0
	// Upload saves.
	if saveMgr != nil {
		logf("Uploading save files to S3...")
		if err := saveMgr.Upload(context.Background()); err != nil {
			logf("WARNING: save upload failed: %v", err)
			exitCode = 1
		}
	} else {
		logf("S3 not configured — skipping save upload")
	}

	// Shutdown notification.
	if cfg.DiscordConfigured() {
		logf("Sending shutdown notification to Discord...")
		if err := notifier.ServerShutdown(cfg.ServerName); err != nil {
			logf("Failed to send shutdown notification: %v", err)
		}
	}

	logf("Shutdown sequence completed in %s", time.Since(start).Round(time.Second))
	return exitCode
}

// injectRCONPassword writes the RCON password file the base image reads and
// patches server-settings.json, replacing the jq step from the old entrypoint.
func injectRCONPassword(cfg *config.Config) error {
	if cfg.RCONPassword == "" {
		return nil
	}
	logf("Configuring RCON password...")
	if err := os.WriteFile(cfg.RCONPasswordFile, []byte(cfg.RCONPassword), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", cfg.RCONPasswordFile, err)
	}
	if err := patchServerSettingsRCON(cfg.ServerSettingsPath, cfg.RCONPassword); err != nil {
		return fmt.Errorf("patch %s: %w", cfg.ServerSettingsPath, err)
	}
	return nil
}
