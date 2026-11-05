//go:build functional

package catalog

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A real loopback DNS server supplies wire evidence for the standard resolver.
// This parser belongs only to the test fixture; production uses LookupTXT.
func TestPublisherVerificationOwnedDNS(t *testing.T) {
	for _, mode := range []string{"chunks", "tcp", "delegation", "nxdomain", "timeout", "malformed", "record_limit", "byte_limit"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, publisher, ctx := publisherVerificationFixture(t, DomainVerificationOptional)
			challenge := issuePublisherChallenge(t, s, ctx, publisher)
			udp, tcp, err := listenPublisherDNS(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close() })
			var udpCalls, tcpCalls atomic.Int32
			reply := func(raw []byte, stream bool) []byte {
				var request dnsmessage.Message
				if request.Unpack(raw) != nil || len(request.Questions) != 1 {
					return nil
				}
				question := request.Questions[0]
				if mode == "malformed" {
					return []byte{0, 1, 2}
				}
				response := dnsmessage.Message{Header: dnsmessage.Header{ID: request.ID, Response: true, RecursionAvailable: true}, Questions: request.Questions}
				switch {
				case question.Name.String() != challenge.Name || question.Type != dnsmessage.TypeTXT:
					response.RCode = dnsmessage.RCodeNameError
				case (mode == "tcp" || mode == "record_limit" || mode == "byte_limit") && !stream:
					response.Truncated = true
				case mode == "nxdomain":
					response.RCode = dnsmessage.RCodeNameError
				default:
					name := question.Name
					if mode == "delegation" {
						name = dnsmessage.MustNewName("delegated.example.")
						response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.CNAMEResource{CNAME: name}})
					}
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.TXTResource{TXT: []string{DomainTXTValuePrefix, challenge.Value[len(DomainTXTValuePrefix):]}}})
					if mode == "record_limit" || mode == "byte_limit" {
						count := 64
						if mode == "byte_limit" {
							count = 40
						}
						for range count {
							text := "unrelated"
							if mode == "byte_limit" {
								text = strings.Repeat("x", 255)
							}
							response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}, Body: &dnsmessage.TXTResource{TXT: []string{text}}})
						}
					}
				}
				encoded, _ := response.Pack()
				return encoded
			}
			udpDone := make(chan struct{})
			go func() {
				defer close(udpDone)
				buffer := make([]byte, 65535)
				for {
					n, address, err := udp.ReadFrom(buffer)
					if err != nil {
						return
					}
					udpCalls.Add(1)
					if mode != "timeout" {
						_, _ = udp.WriteTo(reply(buffer[:n], false), address)
					}
				}
			}()
			tcpDone := make(chan struct{})
			go func() {
				defer close(tcpDone)
				for {
					connection, err := tcp.Accept()
					if err != nil {
						return
					}
					func() {
						defer func() { _ = connection.Close() }()
						_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
						var length [2]byte
						if _, err := io.ReadFull(connection, length[:]); err != nil {
							return
						}
						buffer := make([]byte, int(binary.BigEndian.Uint16(length[:])))
						if _, err := io.ReadFull(connection, buffer); err != nil {
							return
						}
						tcpCalls.Add(1)
						answer := reply(buffer, true)
						answerSize := len(answer)
						if answerSize > 65535 {
							t.Error("DNS fixture exceeded TCP message limit")
							return
						}
						binary.BigEndian.PutUint16(length[:], uint16(answerSize))
						_, _ = connection.Write(append(length[:], answer...))
					}()
				}
			}()
			t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close(); <-udpDone; <-tcpDone })
			s.publisherTXT = &net.Resolver{PreferGo: true, Dial: func(call context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(call, network, udp.LocalAddr().String())
			}}
			if mode == "timeout" || mode == "malformed" {
				s.publisherDomainPolicy.LookupTimeout = 100 * time.Millisecond
			}
			status, err := s.CheckPublisherDomain(ctx, publisher.ID, "catalog.example.com", "Check owned DNS fixture", challenge.Status.Version)
			expected := "matched"
			if mode == "nxdomain" {
				expected = "not_found"
			}
			if mode == "timeout" || mode == "malformed" {
				expected = "unavailable"
			}
			if mode == "record_limit" || mode == "byte_limit" {
				expected = "response_limit"
			}
			if err != nil || status.LastCheckOutcome != expected || udpCalls.Load() == 0 {
				t.Fatalf("owned DNS: outcome=%s udp=%d tcp=%d err=%v", status.LastCheckOutcome, udpCalls.Load(), tcpCalls.Load(), err)
			}
			if (mode == "tcp" || mode == "record_limit" || mode == "byte_limit") && tcpCalls.Load() == 0 {
				t.Fatal("resolver did not use TCP fallback")
			}
		})
	}
}

// Reserve TCP and UDP together. Their independent ephemeral port ranges can
// overlap under the broad parallel suite, so retry only a real bind collision.
func listenPublisherDNS(ctx context.Context) (net.PacketConn, net.Listener, error) {
	config := net.ListenConfig{}
	for range 16 {
		tcp, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		udp, err := config.ListenPacket(ctx, "udp", tcp.Addr().String())
		if err == nil {
			return udp, tcp, nil
		}
		_ = tcp.Close()
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, nil, err
		}
	}
	return nil, nil, errors.New("owned DNS fixture could not reserve a TCP/UDP port pair")
}
