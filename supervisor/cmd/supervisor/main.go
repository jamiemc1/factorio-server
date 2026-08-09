// Command supervisor is the sole entrypoint for the Factorio server container.
// It installs the Factorio headless build onto the data volume, seeds the
// volume from the defaults baked into the image, injects the RCON password,
// synchronises mods, launches the server (capturing its stdio into an in-memory
// log bus for the web interface), runs a player monitor, serves a small HTTP
// API, and performs a graceful save-and-notify shutdown.
//
// Subcommands:
//
//	supervisor run           launch and supervise the server (default)
//	supervisor install-game  install the Factorio headless build and exit
//	supervisor sync-mods     assemble /factorio/mods and exit
//	supervisor upload        upload saves to S3 and exit
//	supervisor download      download saves from S3 and exit
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/thomasalbrighton/factorio-supervisor/internal/gameinstall"
	"github.com/thomasalbrighton/factorio-supervisor/internal/mods"
	"github.com/thomasalbrighton/factorio-supervisor/internal/monitor"
	"github.com/thomasalbrighton/factorio-supervisor/internal/rcon"
	"github.com/thomasalbrighton/factorio-supervisor/internal/s3"
	"github.com/thomasalbrighton/factorio-supervisor/internal/server"
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
	case "install-game":
		os.Exit(runInstallGame(cfg))
	case "sync-mods":
		os.Exit(runSyncMods(cfg))
	case "upload":
		os.Exit(runSaves(cfg, true))
	case "download":
		os.Exit(runSaves(cfg, false))
	default:
		logf("FATAL: unknown subcommand %q (want run|install-game|sync-mods|upload|download)", cmd)
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

// newInstaller builds the game installer, which uses S3 as a cold-start cache
// for the headless tarball when it is configured.
func newInstaller(cfg *config.Config) *gameinstall.Installer {
	return gameinstall.New(gameinstall.Config{
		GameDir: cfg.GameDir,
		Store:   newStore(cfg),
		Log:     logf,
	})
}

func runInstallGame(cfg *config.Config) int {
	dir, err := newInstaller(cfg).Ensure(context.Background(), cfg.FactorioVersion)
	if err != nil {
		logf("FATAL: game install failed: %v", err)
		return 1
	}
	logf("Factorio installed at %s", dir)
	return 0
}

func runSyncMods(cfg *config.Config) int {
	ctx := context.Background()
	m := mods.New(mods.Config{
		ModsDir:     cfg.ModsDir,
		PrivateDir:  cfg.PrivateModsDir,
		PublicJSON:  cfg.PublicModsJSON,
		Username:    cfg.FactorioUsername,
		Token:       cfg.FactorioToken,
		DLCSpaceAge: cfg.DLCSpaceAge,
		Store:       newStore(cfg),
		Log:         logf,
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

	launcher := server.New(cfg, logf)

	// Create the volume tree and lay the image's baked config/mod defaults over
	// it. The volume mounts empty, so nothing works until this has run.
	if err := launcher.SeedVolume(); err != nil {
		logf("FATAL: %v", err)
		return 1
	}
	// server-settings.json only exists once the volume is seeded, so the
	// credential fallback has to run again now.
	cfg.ResolvePortalCredentials()

	if err := injectRCONPassword(cfg); err != nil {
		logf("FATAL: could not configure RCON password: %v", err)
		return 1
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

	// Install the game onto the volume. On a warm volume this is a stat call;
	// on a cold one it pulls the headless tarball from the S3 cache or
	// factorio.com.
	logf("Ensuring Factorio %s is installed...", cfg.FactorioVersion)
	installDir, err := newInstaller(cfg).Ensure(context.Background(), cfg.FactorioVersion)
	if err != nil {
		logf("FATAL: game install failed: %v", err)
		return 1
	}
	configPath, err := launcher.WriteConfigINI(installDir)
	if err != nil {
		logf("FATAL: could not write config.ini: %v", err)
		return 1
	}

	var saveMgr *s3.SaveManager
	if store != nil {
		saveMgr = s3.NewSaveManager(store, cfg.SavesDir, cfg.SaveName, logf)
	}

	// Seed the volume's saves from S3 when they are missing or stale, so a
	// machine that lost its volume still boots the newest world. A missing save
	// is not fatal (fresh world); a transport error is, to avoid silently
	// starting from a stale local save and then overwriting the good S3 copy on
	// shutdown.
	if saveMgr != nil {
		logf("Checking S3 for a newer save...")
		if err := saveMgr.DownloadIfNewer(context.Background()); err != nil {
			logf("FATAL: save download failed — aborting to avoid clobbering the S3 save: %v", err)
			return 1
		}
	} else {
		logf("S3 not configured — skipping save download")
	}

	// Synchronise mods before launch; a failure aborts to avoid loading a
	// mismatched mod set (matching the previous entrypoint behaviour).
	modMgr := mods.New(mods.Config{
		ModsDir:     cfg.ModsDir,
		PrivateDir:  cfg.PrivateModsDir,
		PublicJSON:  cfg.PublicModsJSON,
		Username:    cfg.FactorioUsername,
		Token:       cfg.FactorioToken,
		DLCSpaceAge: cfg.DLCSpaceAge,
		Store:       store,
		Log:         logf,
	})
	logf("Syncing mods (private + S3-cached public)...")
	if err := modMgr.Sync(context.Background()); err != nil {
		logf("FATAL: Mod sync failed — aborting startup: %v", err)
		return 1
	}

	// Clear partial saves and generate a map if there is nothing to load — the
	// last of the work the base image's entrypoint used to do.
	if err := launcher.CleanTempSaves(); err != nil {
		logf("FATAL: could not clean temporary saves: %v", err)
		return 1
	}
	if err := launcher.EnsureSave(installDir, configPath); err != nil {
		logf("FATAL: %v", err)
		return 1
	}

	args, err := launcher.Args(installDir, configPath, os.Args[2:])
	if err != nil {
		logf("FATAL: could not build server arguments: %v", err)
		return 1
	}

	// Launch Factorio, capturing its stdio into the log bus.
	bus := factorio.NewLogBus(2000)
	proc := factorio.NewProcess(bus, gameinstall.Binary(installDir), args, logf)
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

// injectRCONPassword writes the RCON password file the launcher reads and
// patches server-settings.json, replacing the jq step from the old entrypoint.
// When RCON_PASSWORD is unset it generates one (the base image used pwgen), so
// RCON is always reachable for the monitor and the web console.
func injectRCONPassword(cfg *config.Config) error {
	if cfg.RCONPassword == "" {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return fmt.Errorf("generate RCON password: %w", err)
		}
		cfg.RCONPassword = hex.EncodeToString(buf)
		logf("RCON_PASSWORD not set — generated one for this boot")
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
