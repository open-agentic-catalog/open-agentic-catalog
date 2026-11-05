// The local fixture command serves only validated, prepared sample archives.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Demo sample tool failed (stage: %s).\n", startupStage(err))
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("demo-archives", flag.ContinueOnError)
	directory := flags.String("directory", "output/demo/archives", "private local TLS directory")
	address := flags.String("address", "127.0.0.1:18443", "local fixture listener")
	fixtureIP := flags.String("fixture-ip", "172.30.255.41", "exact private Compose fixture address in the TLS certificate")
	prepare := flags.Bool("prepare", false, "prepare TLS, archives and allowlist without a listener")
	prepareRuntimes := flags.Bool("prepare-runtimes", false, "prepare stable private TLS and separate MCP client key only")
	dataFile := flags.String("data-file", "", "local YAML dataset; empty uses the embedded same-source default")
	profile := flags.String("sample-set", "curated", "manifest profile for preview or preparation")
	check := flags.Bool("check", false, "validate and preview locally without TLS, API or server")
	receipt := flags.String("state-file", "", "optional receipt hints for preview/known original archive preparation")
	origin := flags.String("archive-origin", "https://127.0.0.1:18443", "selected local archive origin for preview")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("demo archive tool requires a non-root user")
	}
	if flags.NArg() != 0 || *check && *prepare || *prepareRuntimes && (*check || *prepare) {
		return errors.New("unexpected arguments")
	}
	if *prepareRuntimes {
		if err := prepareTLSForIP(*directory, *fixtureIP); err != nil {
			return atStartupStage("tls.prepare", err)
		}
		if err := prepareRuntimeKey(*directory); err != nil {
			return err
		}
		fmt.Println("Demo peer TLS and separate private MCP client input prepared.")
		return nil
	}
	if *check || *prepare {
		plan, err := demodata.Load(*dataFile, *profile, *origin)
		if err != nil {
			return atStartupStage("sample.load", err)
		}
		if *check {
			raw, e := demodata.EncodePreview(plan, *receipt)
			if e != nil {
				return e
			}
			_, e = fmt.Fprintln(os.Stdout, string(raw))
			return e
		}
		if err := prepareTLSForIP(*directory, *fixtureIP); err != nil {
			return atStartupStage("tls.prepare", err)
		}
		if err := demodata.PrepareWithReceipt(*directory, plan, *receipt, *origin); err != nil {
			return atStartupStage("prepared.write", err)
		}
	}
	if *prepare {
		fmt.Println("Demo TLS, immutable archives and exact allowlist prepared.")
		return nil
	}
	host, _, err := net.SplitHostPort(*address)
	if err != nil || (host != "127.0.0.1" && host != "0.0.0.0") {
		return atStartupStage("listener.address", errors.New("invalid fixture listener"))
	}
	files, err := demodata.ReadPrepared(*directory)
	if err != nil {
		return atStartupStage("prepared.read", err)
	}
	if err := prepareTLSForIP(*directory, *fixtureIP); err != nil {
		return atStartupStage("tls.prepare", err)
	}
	server := &http.Server{Addr: *address, Handler: demofixture.PreparedHandler(files), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		timeout, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(timeout)
	}()
	fmt.Println("Serving the prepared local demo archives over HTTPS.")
	err = server.ListenAndServeTLS(filepath.Join(*directory, "cert.pem"), filepath.Join(*directory, "key.pem"))
	stop()
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return atStartupStage("listener.address-in-use", err)
	}
	return atStartupStage("listener.serve", err)
}

// prepareTLS refuses partial, linked or permissively stored input instead of silently rotating trust.
func prepareTLS(directory string) error { return prepareTLSForIP(directory, "172.30.255.41") }

func prepareTLSForIP(directory, fixtureIP string) error {
	ip, err := netip.ParseAddr(fixtureIP)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.String() != fixtureIP {
		return errors.New("fixture certificate requires a canonical private IPv4 address")
	}
	if err := demodata.PrepareDirectory(directory); err != nil {
		return err
	}
	certPath, keyPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem")
	certInfo, certErr := os.Lstat(certPath)
	keyInfo, keyErr := os.Lstat(keyPath)
	if certErr == nil || keyErr == nil {
		if certErr != nil || keyErr != nil || !certInfo.Mode().IsRegular() || !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm() != 0600 || certInfo.Size() > 16384 || keyInfo.Size() > 16384 {
			return errors.New("invalid existing fixture TLS files")
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return err
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return err
		}
		return validateExistingCertificate(leaf, fixtureIP, time.Now())
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return errors.New("cannot inspect fixture TLS files")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Local Catalog demo archives"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(365 * 24 * time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP(fixtureIP)}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	// Exclusive file creation avoids replacing existing trust or following symlinks.
	for _, file := range []struct {
		path, kind string
		raw        []byte
		mode       os.FileMode
	}{{keyPath, "PRIVATE KEY", rawKey, 0600}, {certPath, "CERTIFICATE", cert, 0644}} {
		f, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.mode)
		if err != nil {
			return err
		}
		err = pem.Encode(f, &pem.Block{Type: file.kind, Bytes: file.raw})
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateExistingCertificate(leaf *x509.Certificate, fixtureIP string, now time.Time) error {
	if now.Before(leaf.NotBefore) {
		return atStartupStage("tls.not-yet-valid", errors.New("existing fixture certificate is not yet valid"))
	}
	if !now.Before(leaf.NotAfter) {
		return atStartupStage("tls.expired", errors.New("existing fixture certificate is expired"))
	}
	if leaf.VerifyHostname("127.0.0.1") != nil || leaf.VerifyHostname(fixtureIP) != nil {
		return atStartupStage("tls.hostname", errors.New("existing fixture certificate is mismatched"))
	}
	return nil
}
