package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// publisherDNS answers only owned .test TXT questions. It never forwards DNS.
// The small wire codec keeps the offline fixture image standard-library-only.
type publisherDNS struct {
	mu      sync.Mutex
	records map[string]string
	queries int
}

func newPublisherDNS() *publisherDNS { return &publisherDNS{records: make(map[string]string)} }

func (s *publisherDNS) control(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF ||
		!strings.HasPrefix(input.Name, "_open-agentic-catalog-challenge.") ||
		!strings.HasSuffix(input.Name, ".test.") || len(input.Name) > 254 || len(input.Value) > 255 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) >= 8 && s.records[input.Name] == "" && input.Value != "" {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if input.Value == "" {
		delete(s.records, input.Name)
	} else {
		s.records[input.Name] = input.Value
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *publisherDNS) report(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Queries int `json:"queries"`
	}{s.queries}); err != nil {
		return
	}
}

func (s *publisherDNS) answer(raw []byte) []byte {
	if len(raw) < 17 || raw[2]&0xf8 != 0 || binary.BigEndian.Uint16(raw[4:6]) != 1 {
		return nil
	}
	var labels []string
	offset := 12
	for {
		if offset >= len(raw) {
			return nil
		}
		n := int(raw[offset])
		offset++
		if n == 0 {
			break
		}
		if n > 63 || offset+n > len(raw) {
			return nil
		}
		labels = append(labels, string(raw[offset:offset+n]))
		offset += n
	}
	if offset+4 > len(raw) {
		return nil
	}
	name := strings.ToLower(strings.Join(labels, ".")) + "."
	txt := binary.BigEndian.Uint16(raw[offset:]) == 16 && binary.BigEndian.Uint16(raw[offset+2:]) == 1
	response := append([]byte(nil), raw[:offset+4]...)
	response[2], response[3] = 0x81, 0x83 // Response, recursion available, NXDOMAIN.
	clear(response[6:12])
	s.mu.Lock()
	defer s.mu.Unlock()
	if txt && strings.HasPrefix(name, "_open-agentic-catalog-challenge.") {
		s.queries++
	}
	value := s.records[name]
	valueSize := len(value)
	if txt && valueSize > 0 && valueSize <= 255 {
		response[3] = 0x80
		response[7] = 1
		// Answer points to the uncompressed question. TTL zero avoids fixture caching.
		response = append(response, 0xc0, 0x0c, 0, 16, 0, 1, 0, 0, 0, 0)
		response = binary.BigEndian.AppendUint16(response, uint16(valueSize)+1)
		response = append(response, byte(valueSize))
		response = append(response, value...)
	}
	return response
}

func (s *publisherDNS) listen(ctx context.Context, address string) (func(), error) {
	udp, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp", address)
	if err != nil {
		return nil, err
	}
	tcp, err := (&net.ListenConfig{}).Listen(ctx, "tcp", udp.LocalAddr().String())
	if err != nil {
		_ = udp.Close()
		return nil, err
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		buffer := make([]byte, 4096)
		for {
			n, peer, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			if answer := s.answer(buffer[:n]); answer != nil {
				_, _ = udp.WriteTo(answer, peer)
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var size [2]byte
				if _, err := io.ReadFull(conn, size[:]); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint16(size[:]))
				if n > 4096 {
					return
				}
				buffer := make([]byte, n)
				if _, err := io.ReadFull(conn, buffer); err != nil {
					return
				}
				answer := s.answer(buffer)
				answerSize := len(answer)
				if answerSize > 0 && answerSize <= 65535 {
					binary.BigEndian.PutUint16(size[:], uint16(answerSize))
					_, _ = conn.Write(append(size[:], answer...))
				}
			}()
		}
	}()
	return func() { _ = udp.Close(); _ = tcp.Close(); workers.Wait() }, nil
}
