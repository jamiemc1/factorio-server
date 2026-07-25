// Package discord sends webhook embed notifications for Factorio server events,
// mirroring the messages the previous Python discord_notifier produced.
package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Discord embed colors are decimal ints; these match the hex codes used before.
const (
	colorGreen = 0x00ff00
	colorRed   = 0xff0000
	colorBlue  = 0x03b2f8
	colorAmber = 0xffa500
)

// Notifier posts embeds to a Discord webhook. A zero/empty webhook makes every
// method a no-op so callers need not branch.
type Notifier struct {
	webhookURL string
	client     *http.Client
}

// New returns a Notifier. If webhookURL is empty, sends are silently skipped.
func New(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 15 * time.Second},
	}
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
	Fields      []embedField `json:"fields,omitempty"`
}

type payload struct {
	Embeds []embed `json:"embeds"`
}

func (n *Notifier) send(e embed) error {
	if n.webhookURL == "" {
		return nil
	}
	e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(payload{Embeds: []embed{e}})
	if err != nil {
		return err
	}
	resp, err := n.client.Post(n.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned %s", resp.Status)
	}
	return nil
}

// ServerStartup announces the server is online.
func (n *Notifier) ServerStartup(serverName string) error {
	return n.send(embed{
		Title:       fmt.Sprintf("🚀 %s Started", serverName),
		Description: "The Factorio server is now online and ready for players!",
		Color:       colorGreen,
	})
}

// ServerShutdown announces the server is shutting down.
func (n *Notifier) ServerShutdown(serverName string) error {
	return n.send(embed{
		Title:       fmt.Sprintf("🛑 %s Shutting Down", serverName),
		Description: "The Factorio server is shutting down.",
		Color:       colorRed,
	})
}

// PlayerJoined announces a single player joining.
func (n *Notifier) PlayerJoined(player string) error {
	return n.send(embed{
		Title:       "➕ Player Joined",
		Description: fmt.Sprintf("**%s** has joined the server", player),
		Color:       colorGreen,
	})
}

// PlayerLeft announces a single player leaving.
func (n *Notifier) PlayerLeft(player string) error {
	return n.send(embed{
		Title:       "➖ Player Left",
		Description: fmt.Sprintf("**%s** has left the server", player),
		Color:       colorAmber,
	})
}

// PlayerStatus posts a periodic roster of who is online.
func (n *Notifier) PlayerStatus(serverName string, players []string) error {
	e := embed{
		Title: fmt.Sprintf("👥 %s - Player Status", serverName),
		Color: colorBlue,
	}
	if len(players) == 0 {
		e.Description = "No players currently online."
	} else {
		noun := "players"
		if len(players) == 1 {
			noun = "player"
		}
		e.Description = fmt.Sprintf("%d %s online", len(players), noun)
		var b bytes.Buffer
		for i, p := range players {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "• %s", p)
		}
		e.Fields = []embedField{{Name: "Players", Value: b.String(), Inline: false}}
	}
	return n.send(e)
}
