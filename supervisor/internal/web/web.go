// Package web exposes a minimal HTTP interface over the supervisor: server
// health, a snapshot + live stream of captured Factorio output, the current
// player list, and an RCON command endpoint. It is intentionally small — the
// plumbing (log bus + RCON) is proven here so a richer UI can be layered on
// later.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/thomasalbrighton/factorio-supervisor/internal/factorio"
	"github.com/thomasalbrighton/factorio-supervisor/internal/rcon"
)

// Server wires the HTTP handlers to the log bus and RCON client.
type Server struct {
	bus        *factorio.LogBus
	rcon       *rcon.Client
	serverName string
	logf       func(string, ...any)
}

// New builds a web Server.
func New(bus *factorio.LogBus, rconClient *rcon.Client, serverName string, logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{bus: bus, rcon: rconClient, serverName: serverName, logf: logf}
}

// Handler returns the HTTP mux for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/logs", s.handleLogsSnapshot)
	mux.HandleFunc("/logs/stream", s.handleLogsStream)
	mux.HandleFunc("/players", s.handlePlayers)
	mux.HandleFunc("/rcon", s.handleRCON)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"server": s.serverName,
		"time":   time.Now().UTC(),
	})
}

// handleLogsSnapshot returns the retained log ring buffer as JSON.
func (s *Server) handleLogsSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.bus.History())
}

// handleLogsStream streams live log lines to the client using Server-Sent
// Events, prefixed with the current backlog.
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	history, lines, cancel := s.bus.Subscribe(512)
	defer cancel()

	for _, line := range history {
		writeSSE(w, line)
	}
	flusher.Flush()

	ctx := r.Context()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			writeSSE(w, line)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	players, err := s.rcon.OnlinePlayers()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if players == nil {
		players = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": players, "count": len(players)})
}

// handleRCON runs an arbitrary RCON command supplied as {"command": "..."}.
func (s *Server) handleRCON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected JSON body {\"command\": \"...\"}"})
		return
	}
	out, err := s.rcon.Execute(req.Command)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"response": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, line factorio.LogLine) {
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", line.Seq, data)
}
