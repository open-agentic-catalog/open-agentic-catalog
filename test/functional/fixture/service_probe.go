package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// serviceProbe reads private inputs from stdin and emits only fixed outcomes and
// process identifiers. It has no proxy, DNS, redirect, tunnel or retry fallback.
func serviceProbe(input io.Reader, output io.Writer) error {
	var in struct {
		Address  string `json:"address"`
		Token    string `json:"token"`
		Requests int    `json:"requests"`
	}
	decoder := json.NewDecoder(io.LimitReader(input, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid Service probe input")
	}
	address, err := netip.ParseAddrPort(in.Address)
	if err != nil || !address.Addr().IsPrivate() || address.Port() != 8080 || in.Token == "" || (in.Requests != 1 && in.Requests != 48) {
		return errors.New("invalid Service probe bounds")
	}
	client, transport := serviceProbeClient(in.Address)
	defer transport.CloseIdleConnections()
	outcomes := make([]serviceProbeOutcome, 0, in.Requests)
	for range in.Requests {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+in.Address+"/api/v1/service-information", nil)
		if err != nil {
			return errors.New("invalid probe request")
		}
		request.Header.Set("Authorization", "Bearer "+in.Token)
		response, requestErr := client.Do(request)
		outcome := serviceProbeOutcome{Outcome: "transport_error"}
		if requestErr != nil {
			var timeout net.Error
			if errors.As(requestErr, &timeout) && timeout.Timeout() {
				outcome.Outcome = "timeout"
			}
		} else {
			outcome = serviceInformationOutcome(response.StatusCode, io.LimitReader(response.Body, 16<<10))
			_ = response.Body.Close()
		}
		outcomes = append(outcomes, outcome)
	}
	return json.NewEncoder(output).Encode(outcomes)
}

type serviceProbeOutcome struct {
	Outcome    string `json:"outcome"`
	InstanceID string `json:"instanceId,omitempty"`
}

func serviceInformationOutcome(status int, body io.Reader) serviceProbeOutcome {
	var info struct {
		Service    string `json:"service"`
		Role       string `json:"role"`
		InstanceID string `json:"instanceId"`
	}
	decoder := json.NewDecoder(body)
	if status != http.StatusOK || decoder.Decode(&info) != nil || decoder.Decode(new(any)) != io.EOF || info.Service != "open-agentic-catalog" || info.Role != "api" || !probeProcessID(info.InstanceID) {
		return serviceProbeOutcome{Outcome: "invalid_response"}
	}
	return serviceProbeOutcome{Outcome: "ok", InstanceID: info.InstanceID}
}

// Accept only the bounded UUID alphabet, never arbitrary response text.
func probeProcessID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func serviceProbeClient(address string) (*http.Client, *http.Transport) {
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, destination string) (net.Conn, error) {
		if network != "tcp" || destination != address {
			return nil, errors.New("unexpected probe destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, destination)
	}}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport
}
