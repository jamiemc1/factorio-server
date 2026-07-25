// Package monitor polls the Factorio server over RCON for the online player
// roster, emits Discord join/leave/status notifications, and periodically
// uploads the save if it has changed. It mirrors player_monitor.py.
package monitor

import (
	"context"
	"time"

	"github.com/thomasalbrighton/factorio-supervisor/internal/discord"
	"github.com/thomasalbrighton/factorio-supervisor/internal/rcon"
	"github.com/thomasalbrighton/factorio-supervisor/internal/s3"
)

// Monitor holds the polling configuration and dependencies.
type Monitor struct {
	rcon        *rcon.Client
	notifier    *discord.Notifier
	saves       *s3.SaveManager // may be nil if S3 not configured
	serverName  string
	interval    time.Duration
	notifyJoin  bool
	notifyStat  bool
	logf        func(string, ...any)

	prev         map[string]bool
	lastSaveMtime int64
}

// Config configures a Monitor.
type Config struct {
	RCON          *rcon.Client
	Notifier      *discord.Notifier
	Saves         *s3.SaveManager
	ServerName    string
	Interval      time.Duration
	NotifyJoin    bool
	NotifyStatus  bool
	Log           func(string, ...any)
}

// New builds a Monitor.
func New(cfg Config) *Monitor {
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Monitor{
		rcon:       cfg.RCON,
		notifier:   cfg.Notifier,
		saves:      cfg.Saves,
		serverName: cfg.ServerName,
		interval:   cfg.Interval,
		notifyJoin: cfg.NotifyJoin,
		notifyStat: cfg.NotifyStatus,
		logf:       logf,
		prev:       map[string]bool{},
	}
}

// Run polls until ctx is cancelled. It performs one check immediately, then on
// each interval tick.
func (m *Monitor) Run(ctx context.Context) {
	m.logf("Player monitor started (interval=%s)", m.interval)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			m.logf("Player monitor stopped")
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Monitor) tick(ctx context.Context) {
	m.checkPlayers()
	m.uploadSaveIfChanged(ctx)
}

func (m *Monitor) checkPlayers() {
	players, err := m.rcon.OnlinePlayers()
	if err != nil {
		m.logf("Error checking players: %v", err)
		return
	}
	current := make(map[string]bool, len(players))
	for _, p := range players {
		current[p] = true
	}

	if m.notifyJoin {
		for p := range current {
			if !m.prev[p] {
				if err := m.notifier.PlayerJoined(p); err != nil {
					m.logf("discord join notify failed: %v", err)
				}
			}
		}
		for p := range m.prev {
			if !current[p] {
				if err := m.notifier.PlayerLeft(p); err != nil {
					m.logf("discord leave notify failed: %v", err)
				}
			}
		}
	}

	if m.notifyStat {
		if err := m.notifier.PlayerStatus(m.serverName, players); err != nil {
			m.logf("discord status notify failed: %v", err)
		}
	}

	m.prev = current
}

func (m *Monitor) uploadSaveIfChanged(ctx context.Context) {
	if m.saves == nil {
		return
	}
	mtime, uploaded, err := m.saves.UploadIfChanged(ctx, m.lastSaveMtime)
	if err != nil {
		m.logf("Periodic save upload failed: %v", err)
		return
	}
	if uploaded {
		m.lastSaveMtime = mtime
	}
}
