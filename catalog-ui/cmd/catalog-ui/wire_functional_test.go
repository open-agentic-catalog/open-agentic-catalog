//go:build functional

package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUIConfiguredListenerBoundsWireHeaders(t *testing.T) {
	filename, _ := commandConfig(t)
	err := runHost(filename, func(server *http.Server, _ func()) error {
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() {
			_ = server.Close()
			if err := <-done; !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve: %v", err)
			}
		}()
		for _, tc := range []struct {
			raw    string
			status int
		}{
			{"GET /livez HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n", 200},
			{"GET /livez HTTP/1.1\r\nHost: local\r\nX-Fill: " + strings.Repeat("x", server.MaxHeaderBytes+8192) + "\r\n\r\n", 431},
			{"GET /livez HTTP/1.1\r\nHost: local\r\nX-Incomplete: ", 0},
			{"GET /livez HTTP/1.1\r\nHost: local\r\nConnection: close\r\n\r\n", 200},
		} {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", listener.Addr().String())
			if err != nil {
				return err
			}
			if err := conn.SetDeadline(time.Now().Add(13 * time.Second)); err != nil {
				_ = conn.Close()
				return err
			}
			if _, err := io.WriteString(conn, tc.raw); err != nil {
				_ = conn.Close()
				return err
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			_ = conn.Close()
			if tc.status == 0 {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("incomplete header not closed: %v", err)
				}
				continue
			}
			if err != nil {
				return err
			}
			_ = response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d", response.StatusCode, tc.status)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
