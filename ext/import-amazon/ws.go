package amazon

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// A WebSocket client with exactly the surface the DevTools Protocol needs:
// dial, send a text frame, receive a message, close. RFC 6455 is about two
// hundred lines when you only need the client side, which is why this module
// does not carry a dependency for it — rule 2 says ext/ adds none, and the
// standard library has no WebSocket of its own.
//
// What is deliberately not here: extensions, compression, subprotocols, and
// binary frames larger than the DevTools Protocol ever sends. Chrome speaks
// plain text frames on a loopback socket; anything more would be surface with
// no caller.

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA

	// A page's extracted JSON is a few hundred kilobytes at most; a frame a
	// hundred times that is not DevTools talking.
	maxWSMessage = 64 << 20
)

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
}

// dialWS opens a WebSocket to a ws:// URL and completes the opening handshake.
func dialWS(ctx context.Context, rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("websocket: %q is not a URL: %w", rawURL, err)
	}
	if u.Scheme != "ws" {
		// wss would need TLS, and the DevTools endpoint is loopback plain text.
		return nil, fmt.Errorf("websocket: scheme %q is not ws", u.Scheme)
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("websocket: dial %s: %w", u.Host, err)
	}
	c := &wsConn{conn: conn, r: bufio.NewReader(conn)}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce)

	request := "GET " + u.RequestURI() + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake: %w", err)
	}

	// Read the response through the same buffered reader the frames will use:
	// Chrome may send the first frame in the same packet as the 101, and a
	// second reader would swallow it.
	resp, err := http.ReadResponse(c.r, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake refused: %s", resp.Status)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if want := base64.StdEncoding.EncodeToString(sum[:]); resp.Header.Get("Sec-WebSocket-Accept") != want {
		conn.Close()
		return nil, errors.New("websocket: handshake accept key did not match")
	}
	return c, nil
}

// writeText sends one masked text frame. Clients must mask; servers must not.
func (c *wsConn) writeText(payload []byte) error {
	return c.writeFrame(opText, payload)
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode) // FIN set: no fragmentation on the way out
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126)
		header = binary.BigEndian.AppendUint16(header, uint16(n))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)

	masked := make([]byte, n)
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// readMessage returns the next complete data message, reassembling fragments
// and answering pings along the way. A close frame is returned as io.EOF.
func (c *wsConn) readMessage() ([]byte, error) {
	var message []byte
	var inProgress bool
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			// Echo the close so the peer's own close completes cleanly.
			_ = c.writeFrame(opClose, payload)
			return nil, io.EOF
		case opText, opBinary:
			if inProgress {
				return nil, errors.New("websocket: new message began before the last one finished")
			}
			message = append(message[:0], payload...)
			inProgress = true
		case opContinuation:
			if !inProgress {
				return nil, errors.New("websocket: continuation frame with nothing to continue")
			}
			message = append(message, payload...)
		default:
			return nil, fmt.Errorf("websocket: unknown opcode %#x", opcode)
		}
		if len(message) > maxWSMessage {
			return nil, errors.New("websocket: message exceeds the size limit")
		}
		if fin {
			return message, nil
		}
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxWSMessage {
		return false, 0, nil, errors.New("websocket: frame exceeds the size limit")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (c *wsConn) close() error {
	_ = c.writeFrame(opClose, nil)
	return c.conn.Close()
}

// wsURLFromHTTP rewrites the DevTools endpoint's http:// form into ws://, which
// is what /json/version hands back in one field and expects in the other.
func wsURLFromHTTP(raw string) string {
	return strings.Replace(raw, "http://", "ws://", 1)
}
