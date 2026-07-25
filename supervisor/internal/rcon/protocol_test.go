package rcon

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// fakeServer implements just enough of the Source RCON protocol to exercise the
// client's framing: it echoes auth with a matching id and answers exec commands
// with a canned body.
func fakeServer(t *testing.T, password, execReply string) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			id, ptype, body, err := readPacket(conn)
			if err != nil {
				return
			}
			switch ptype {
			case typeAuth:
				respID := id
				if body != password {
					respID = -1
				}
				_ = writePacket(conn, respID, typeAuthResponse, "")
			case typeExecCommand:
				_ = writePacket(conn, id, typeResponseValue, execReply)
			}
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func readPacket(r io.Reader) (id, ptype int32, body string, err error) {
	var length int32
	if err = binary.Read(r, binary.LittleEndian, &length); err != nil {
		return
	}
	payload := make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(payload[0:4]))
	ptype = int32(binary.LittleEndian.Uint32(payload[4:8]))
	body = string(bytes.TrimRight(payload[8:], "\x00"))
	return
}

func writePacket(w io.Writer, id, ptype int32, body string) error {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, id)
	_ = binary.Write(&buf, binary.LittleEndian, ptype)
	buf.WriteString(body)
	buf.WriteByte(0)
	buf.WriteByte(0)
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, int32(buf.Len()))
	out.Write(buf.Bytes())
	_, err := w.Write(out.Bytes())
	return err
}

func TestClientAuthAndExecute(t *testing.T) {
	addr, stop := fakeServer(t, "secret", "Online players (1): alice")
	defer stop()

	host, port := splitHostPort(t, addr)
	c := New(host, port, "secret")
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	players, err := c.OnlinePlayers()
	if err != nil {
		t.Fatalf("online players: %v", err)
	}
	if len(players) != 1 || players[0] != "alice" {
		t.Fatalf("players = %v, want [alice]", players)
	}
}

func TestClientBadPassword(t *testing.T) {
	addr, stop := fakeServer(t, "secret", "")
	defer stop()
	host, port := splitHostPort(t, addr)
	c := New(host, port, "wrong")
	if err := c.Connect(); err == nil {
		c.Close()
		t.Fatal("expected auth failure with wrong password")
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, err = fmtSscan(portStr, &port)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func fmtSscan(s string, v *int) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	*v = n
	return 1, nil
}
