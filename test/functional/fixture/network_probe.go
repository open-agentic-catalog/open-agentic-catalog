package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// networkProbe is an explicitly invoked test-only client. Fixed output separates
// dropped TCP connections from exec failures, refused listeners and DNS errors.
// It is never part of a Catalog runtime image.
func networkProbe(args []string, output io.Writer) error {
	if len(args) == 1 && args[0] == "hold" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		return nil
	}
	if len(args) != 2 {
		return errors.New("network-probe requires tcp|dns-udp|dns-tcp and a destination")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	switch args[0] {
	case "tcp":
		address, err := netip.ParseAddrPort(args[1])
		if err != nil || address.Port() == 0 {
			return errors.New("TCP probe requires a numeric address and nonzero port")
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address.String())
		if err == nil {
			_ = conn.Close()
		}
		result, err := networkProbeOutcome(err)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, result)
		return err
	case "dns-udp", "dns-tcp":
		transport := args[0][4:]
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, transport, address)
		}}
		addresses, err := resolver.LookupIPAddr(ctx, args[1])
		if err != nil || len(addresses) == 0 {
			return errors.New("DNS probe did not resolve its destination")
		}
		_, err = fmt.Fprintln(output, "connected")
		return err
	default:
		return errors.New("unknown network probe mode")
	}
}

func networkProbeOutcome(err error) (string, error) {
	if err == nil {
		return "connected", nil
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout", nil
	}
	return "", errors.New("TCP probe failed without a timeout")
}
