// Package config parses and validates all environment-driven configuration for
// the Factorio supervisor. Every os.Getenv lookup lives here so the rest of the
// program takes a validated Config value and never touches the environment.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Factorio server-side integration.
	ServerName string // display name used in Discord messages

	// RCON.
	RCONHost     string
	RCONPort     int
	RCONPassword string

	// Discord.
	DiscordWebhook       string
	DiscordNotifications bool

	// S3-compatible object storage (Tigris on Fly, etc).
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSEndpointURL     string
	AWSBucketName      string
	SaveName           string

	// Factorio portal credentials for downloading public mods.
	FactorioUsername string
	FactorioToken    string

	// Player monitor.
	MonitorInterval  time.Duration
	MonitorJoinLeave bool
	MonitorStatus    bool

	// Optional web interface.
	WebEnabled bool
	WebAddr    string

	// Game install. FactorioVersion is an exact "x.y.z" or a channel keyword
	// ("experimental" / "stable") resolved against the Factorio releases API.
	FactorioVersion string
	GameDir         string
	DefaultsDir     string

	// Server launch options, previously handled by the base image's
	// /docker-entrypoint.sh.
	GamePort           int
	LoadLatestSave     bool
	GenerateNewSave    bool
	Preset             string
	Bind               string
	ConsoleLogLocation string
	DLCSpaceAge        string

	// Paths (overridable mainly for tests; sensible container defaults).
	SavesDir            string
	ModsDir             string
	PrivateModsDir      string
	PublicModsJSON      string
	ScenariosDir        string
	ScriptOutputDir     string
	ConfigDir           string
	ServerSettingsPath  string
	RCONPasswordFile    string
	ShutdownGracePeriod time.Duration
}

// Load reads configuration from the environment, applying the same defaults the
// Python scripts used so behaviour is unchanged.
func Load() (*Config, error) {
	c := &Config{
		ServerName:           getenv("SERVER_NAME", "Factorio Server"),
		RCONHost:             getenv("RCON_HOST", "localhost"),
		RCONPort:             getenvInt("RCON_PORT", 27015),
		RCONPassword:         os.Getenv("RCON_PASSWORD"),
		DiscordWebhook:       os.Getenv("DISCORD_WEBHOOK"),
		DiscordNotifications: getenvBool("DISCORD_NOTIFICATIONS", true),
		AWSAccessKeyID:       os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretAccessKey:   os.Getenv("AWS_SECRET_ACCESS_KEY"),
		AWSEndpointURL:       os.Getenv("AWS_ENDPOINT_URL_S3"),
		AWSBucketName:        os.Getenv("AWS_BUCKET_NAME"),
		SaveName:             getenv("SAVE_NAME", "_autosave1"),
		FactorioUsername:     os.Getenv("FACTORIO_USERNAME"),
		FactorioToken:        os.Getenv("FACTORIO_TOKEN"),
		MonitorInterval:      time.Duration(getenvInt("MONITOR_INTERVAL", 600)) * time.Second,
		MonitorJoinLeave:     getenvBool("MONITOR_JOIN_LEAVE", true),
		MonitorStatus:        getenvBool("MONITOR_STATUS", false),
		WebEnabled:           getenvBool("WEB_ENABLED", true),
		WebAddr:              getenv("WEB_ADDR", ":8080"),
		FactorioVersion:      getenv("FACTORIO_VERSION", "experimental"),
		GameDir:              getenv("GAME_DIR", "/factorio/game"),
		DefaultsDir:          getenv("DEFAULTS_DIR", "/opt/defaults"),
		GamePort:             getenvInt("PORT", 34197),
		LoadLatestSave:       getenvBool("LOAD_LATEST_SAVE", true),
		GenerateNewSave:      getenvBool("GENERATE_NEW_SAVE", false),
		Preset:               os.Getenv("PRESET"),
		Bind:                 os.Getenv("BIND"),
		ConsoleLogLocation:   os.Getenv("CONSOLE_LOG_LOCATION"),
		DLCSpaceAge:          getenv("DLC_SPACE_AGE", "true"),
		SavesDir:             getenv("SAVES_DIR", "/factorio/saves"),
		ModsDir:              getenv("MODS_DIR", "/factorio/mods"),
		PrivateModsDir:       getenv("PRIVATE_MODS_DIR", "/factorio/mods/private"),
		PublicModsJSON:       getenv("PUBLIC_MODS_JSON", "/factorio/mods/public.json"),
		ScenariosDir:         getenv("SCENARIOS_DIR", "/factorio/scenarios"),
		ScriptOutputDir:      getenv("SCRIPT_OUTPUT_DIR", "/factorio/script-output"),
		ConfigDir:            getenv("CONFIG_DIR", "/factorio/config"),
		ServerSettingsPath:   getenv("SERVER_SETTINGS_PATH", "/factorio/config/server-settings.json"),
		RCONPasswordFile:     getenv("RCON_PASSWORD_FILE", "/factorio/config/rconpw"),
		ShutdownGracePeriod:  time.Duration(getenvInt("SHUTDOWN_GRACE_SECONDS", 60)) * time.Second,
	}

	c.ResolvePortalCredentials()
	return c, nil
}

// ResolvePortalCredentials fills in missing mod-portal credentials from
// server-settings.json — the same account works for both the game and the
// portal's download endpoint. It is called again after the volume is seeded,
// because on a cold volume server-settings.json does not exist yet at Load time.
func (c *Config) ResolvePortalCredentials() {
	if c.FactorioUsername != "" && c.FactorioToken != "" {
		return
	}
	u, t, ok := credsFromServerSettings(c.ServerSettingsPath)
	if !ok {
		return
	}
	if c.FactorioUsername == "" {
		c.FactorioUsername = u
	}
	if c.FactorioToken == "" {
		c.FactorioToken = t
	}
}

// S3Configured reports whether enough is set to talk to object storage.
func (c *Config) S3Configured() bool {
	return c.AWSAccessKeyID != "" && c.AWSSecretAccessKey != "" &&
		c.AWSEndpointURL != "" && c.AWSBucketName != ""
}

// DiscordConfigured reports whether Discord notifications can be sent.
func (c *Config) DiscordConfigured() bool {
	return c.DiscordNotifications && c.DiscordWebhook != ""
}

func credsFromServerSettings(path string) (username, token string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var s struct {
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return "", "", false
	}
	if s.Username == "" || s.Token == "" {
		return "", "", false
	}
	return s.Username, s.Token, true
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// Match the shell convention: only "true" (case-insensitive) is true.
	switch v {
	case "true", "True", "TRUE", "1", "yes":
		return true
	default:
		return false
	}
}

// String returns a redacted, human-readable summary for startup logging.
func (c *Config) String() string {
	return fmt.Sprintf(
		"server=%q version=%s rcon=%s:%d discord=%t s3=%t bucket=%q save=%q monitor=%s web=%t@%s",
		c.ServerName, c.FactorioVersion, c.RCONHost, c.RCONPort, c.DiscordConfigured(),
		c.S3Configured(), c.AWSBucketName, c.SaveName, c.MonitorInterval,
		c.WebEnabled, c.WebAddr,
	)
}
