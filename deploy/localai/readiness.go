// Command readiness opens the LocalAI realtime endpoint and succeeds only
// after it receives a session.created event. It intentionally uses only the
// standard library so the readiness check adds no workspace dependency.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1" // WebSocket protocol requires SHA-1 for Sec-WebSocket-Accept.
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func main() {
	endpoint := flag.String("url", "ws://localhost:8080/v1/realtime?model=gpt-realtime", "LocalAI realtime WebSocket URL")
	timeout := flag.Duration("timeout", 45*time.Second, "maximum time allowed for the handshake and session.created event")
	flag.Parse()

	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "timeout must be positive")
		os.Exit(2)
	}

	if err := check(*endpoint, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "realtime readiness failed for %s: %v\n", *endpoint, err)
		os.Exit(1)
	}

	fmt.Printf("ready: %s (session.created)\n", *endpoint)
}

func check(endpoint string, timeout time.Duration) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.User != nil {
		return errors.New("URL user information is not supported")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("URL scheme %q must be ws or wss", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("URL host is empty")
	}

	deadline := time.Now().Add(timeout)
	conn, err := dial(u, timeout)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set deadline: %w", err)
	}

	reader, err := handshake(conn, u)
	if err != nil {
		return err
	}
	if err := waitForSessionCreated(conn, reader); err != nil {
		return err
	}
	// Complete the WebSocket close handshake instead of dropping the TCP
	// connection after the readiness event.
	if err := writeClientFrame(conn, 0x8, []byte{0x03, 0xE8}); err != nil {
		return fmt.Errorf("close readiness WebSocket: %w", err)
	}
	return nil
}

func dial(u *url.URL, timeout time.Duration) (net.Conn, error) {
	address := u.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		if strings.Contains(err.Error(), "missing port") {
			port := "80"
			if u.Scheme == "wss" {
				port = "443"
			}
			address = net.JoinHostPort(u.Hostname(), port)
		}
	}

	dialer := &net.Dialer{Timeout: timeout}
	if u.Scheme == "wss" {
		return tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: u.Hostname()})
	}
	return dialer.Dial("tcp", address)
}

func handshake(conn net.Conn, u *url.URL) (*bufio.Reader, error) {
	keyBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, keyBytes); err != nil {
		return nil, fmt.Errorf("make handshake key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	requestURI := u.RequestURI()
	if requestURI == "" {
		requestURI = "/"
	}

	request := "GET " + requestURI + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, fmt.Errorf("write handshake: %w", err)
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("handshake status %s", response.Status)
	}
	if !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") ||
		!headerContains(response.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("handshake response did not confirm WebSocket upgrade")
	}

	hash := sha1.Sum([]byte(key + websocketGUID))
	wantAccept := base64.StdEncoding.EncodeToString(hash[:])
	if response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return nil, errors.New("handshake Sec-WebSocket-Accept mismatch")
	}
	return reader, nil
}

func headerContains(value, want string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}

func waitForSessionCreated(conn net.Conn, reader *bufio.Reader) error {
	var fragments []byte
	var fragmentOpcode byte
	for {
		current, err := readFrame(reader)
		if err != nil {
			return fmt.Errorf("read realtime event: %w", err)
		}

		switch current.opcode {
		case 0x8:
			return errors.New("server closed the WebSocket before session.created")
		case 0x9:
			if err := writeClientFrame(conn, 0xA, current.payload); err != nil {
				return fmt.Errorf("reply to ping: %w", err)
			}
			continue
		case 0xA:
			continue
		case 0x1, 0x2:
			if len(fragments) != 0 {
				return errors.New("received a new data frame before a fragmented message ended")
			}
			fragmentOpcode = current.opcode
			fragments = append(fragments[:0], current.payload...)
		case 0x0:
			if len(fragments) == 0 || fragmentOpcode == 0 {
				return errors.New("received an unexpected continuation frame")
			}
			fragments = append(fragments, current.payload...)
		default:
			return fmt.Errorf("unsupported WebSocket opcode 0x%x", current.opcode)
		}

		if !current.fin {
			continue
		}
		if fragmentOpcode == 0x1 {
			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(fragments, &event); err == nil && event.Type == "session.created" {
				return nil
			}
		}
		fragments = fragments[:0]
		fragmentOpcode = 0
	}
}

func readFrame(reader *bufio.Reader) (frame, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return frame{}, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return frame{}, err
	}

	result := frame{fin: first&0x80 != 0, opcode: first & 0x0F}
	masked := second&0x80 != 0
	length := uint64(second & 0x7F)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	if length > 4<<20 {
		return frame{}, fmt.Errorf("WebSocket frame is too large: %d bytes", length)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return frame{}, err
		}
	}
	result.payload = make([]byte, int(length))
	if _, err := io.ReadFull(reader, result.payload); err != nil {
		return frame{}, err
	}
	if masked {
		for i := range result.payload {
			result.payload[i] ^= mask[i%len(mask)]
		}
	}
	return result, nil
}

func writeClientFrame(conn net.Conn, opcode byte, payload []byte) error {
	key := [4]byte{}
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return err
	}

	header := []byte{0x80 | opcode, 0x80}
	switch {
	case len(payload) < 126:
		header[1] |= byte(len(payload))
	case len(payload) <= 65535:
		header[1] |= 126
		header = append(header, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header[1] |= 127
		header = append(header, make([]byte, 8)...)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	header = append(header, key[:]...)
	maskedPayload := append([]byte(nil), payload...)
	for i := range maskedPayload {
		maskedPayload[i] ^= key[i%len(key)]
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(maskedPayload)
	return err
}
