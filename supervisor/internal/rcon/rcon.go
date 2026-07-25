// Package rcon implements the Source RCON protocol that Factorio speaks, with
// just enough surface to authenticate, run commands, and parse the player list.
// It has no third-party dependencies.
package rcon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Packet types from the Source RCON spec.
const (
	typeAuth          = 3
	typeAuthResponse  = 2
	typeExecCommand   = 2
	typeResponseValue = 0
)

// Client is a single RCON connection. It is safe for concurrent Execute calls;
// requests are serialised so responses match their commands.
type Client struct {
	host, password string
	port           int
	timeout        time.Duration

	mu   sync.Mutex
	conn net.Conn
	id   int32
}

// New returns an unconnected client.
func New(host string, port int, password string) *Client {
	return &Client{host: host, port: port, password: password, timeout: 10 * time.Second}
}

// Connect establishes the TCP connection and authenticates.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *Client) connectLocked() error {
	addr := net.JoinHostPort(c.host, fmt.Sprintf("%d", c.port))
	conn, err := net.DialTimeout("tcp", addr, c.timeout)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	c.conn = conn
	c.id = 0

	respID, err := c.roundtrip(typeAuth, c.password)
	if err != nil {
		c.closeLocked()
		return fmt.Errorf("auth: %w", err)
	}
	// A response id of -1 means authentication failed.
	if respID == -1 {
		c.closeLocked()
		return fmt.Errorf("auth: invalid RCON password")
	}
	return nil
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Execute runs a command, reconnecting once if the connection has dropped.
func (c *Client) Execute(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		if err := c.connectLocked(); err != nil {
			return "", err
		}
	}
	out, err := c.exec(cmd)
	if err != nil {
		// One transparent reconnect+retry on connection error.
		c.closeLocked()
		if cerr := c.connectLocked(); cerr != nil {
			return "", fmt.Errorf("%v (reconnect failed: %w)", err, cerr)
		}
		return c.exec(cmd)
	}
	return out, nil
}

func (c *Client) exec(cmd string) (string, error) {
	c.id++
	reqID := c.id
	if err := c.write(reqID, typeExecCommand, cmd); err != nil {
		return "", err
	}
	_, body, err := c.read()
	if err != nil {
		return "", err
	}
	return body, nil
}

// roundtrip writes a packet and returns the response id (used for auth).
func (c *Client) roundtrip(pType int32, body string) (int32, error) {
	c.id++
	if err := c.write(c.id, pType, body); err != nil {
		return 0, err
	}
	// Auth may be preceded by an empty RESPONSE_VALUE; read until we get the
	// auth response.
	for {
		id, _, err := c.readTyped()
		if err != nil {
			return 0, err
		}
		if id.ptype == typeAuthResponse {
			return id.id, nil
		}
	}
}

func (c *Client) write(id, ptype int32, body string) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.timeout))
	// Packet body: id + type + payload + two null terminators.
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, id)
	_ = binary.Write(&buf, binary.LittleEndian, ptype)
	buf.WriteString(body)
	buf.WriteByte(0)
	buf.WriteByte(0)

	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, int32(buf.Len()))
	out.Write(buf.Bytes())
	_, err := c.conn.Write(out.Bytes())
	return err
}

type header struct {
	id    int32
	ptype int32
}

func (c *Client) read() (int32, string, error) {
	h, body, err := c.readTyped()
	return h.id, body, err
}

func (c *Client) readTyped() (header, string, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	var length int32
	if err := binary.Read(c.conn, binary.LittleEndian, &length); err != nil {
		return header{}, "", err
	}
	if length < 10 || length > 4096+10 {
		return header{}, "", fmt.Errorf("invalid RCON packet length %d", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return header{}, "", err
	}
	var h header
	h.id = int32(binary.LittleEndian.Uint32(payload[0:4]))
	h.ptype = int32(binary.LittleEndian.Uint32(payload[4:8]))
	// Body is everything after the two int32s, minus the two trailing nulls.
	body := payload[8:]
	body = bytes.TrimRight(body, "\x00")
	return h, string(body), nil
}

// OnlinePlayers runs "/players online" and parses the returned names.
//
// Factorio replies with:
//
//	Online players (2):
//	  alice (online)
//	  bob (online)
//
// Older/other formats use a comma-separated single line; both are handled.
func (c *Client) OnlinePlayers() ([]string, error) {
	resp, err := c.Execute("/players online")
	if err != nil {
		return nil, err
	}
	return parsePlayers(resp), nil
}

func parsePlayers(resp string) []string {
	if !strings.Contains(resp, "Online players") {
		return nil
	}
	_, after, ok := strings.Cut(resp, ":")
	if !ok {
		return nil
	}
	rest := strings.TrimSpace(after)
	if rest == "" {
		return nil
	}

	var players []string
	// Multi-line form: one player per line, possibly with a trailing "(online)".
	if strings.Contains(rest, "\n") {
		for line := range strings.SplitSeq(rest, "\n") {
			name := strings.TrimSpace(line)
			name = strings.TrimSuffix(name, "(online)")
			name = strings.TrimSpace(name)
			if name != "" {
				players = append(players, name)
			}
		}
		return players
	}
	// Single-line comma-separated form.
	for part := range strings.SplitSeq(rest, ",") {
		name := strings.TrimSpace(part)
		name = strings.TrimSuffix(name, "(online)")
		name = strings.TrimSpace(name)
		if name != "" {
			players = append(players, name)
		}
	}
	return players
}
