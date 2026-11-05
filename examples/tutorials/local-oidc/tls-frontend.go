// The manual's local-only TLS ingress. It forwards to four fixed Compose
// services and never uses request headers to choose a destination.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		log.Print("Tutorial HTTP transport is unavailable")
		return 1
	}
	servers := make([]*http.Server, 0, 4)
	exitCode := 0
	failures := make(chan error, 4)
	for _, route := range []struct{ host, port, upstream, certificate string }{
		{"catalog-a.test", "8443", "api-a:8080", "catalog-a"},
		{"catalog-b.test", "8445", "api-b:8080", "catalog-b"},
		{"browse-a.test", "9443", "ui-a:8080", "browse-a"},
		{"browse-b.test", "9445", "ui-b:8080", "browse-b"},
	} {
		if os.Getenv("CATALOG_TUTORIAL_SINGLE") == "1" && (route.port == "8445" || route.port == "9445") {
			continue
		}
		transport := defaultTransport.Clone()
		transport.Proxy = nil
		transport.ResponseHeaderTimeout = 30 * time.Second
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(&url.URL{Scheme: "http", Host: route.upstream})
				request.Out.Host = request.In.Host
				for _, name := range []string{"Forwarded", "X-Forwarded-Host", "X-Original-Host", "X-Forwarded-For", "X-Forwarded-Proto"} {
					request.Out.Header.Del(name)
				}
			},
			ErrorLog: log.New(os.Stderr, "", 0),
			ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(writer, "Tutorial upstream unavailable", http.StatusBadGateway)
			},
		}
		server := &http.Server{
			Addr: ":" + route.port,
			Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Host != route.host+":"+route.port || request.URL.IsAbs() {
					http.NotFound(writer, request)
					return
				}
				proxy.ServeHTTP(writer, request)
			}),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
		servers = append(servers, server)
		go func() {
			failures <- server.ListenAndServeTLS("/tls/"+route.certificate+".pem", "/tls/"+route.certificate+"-key.pem")
		}()
	}
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print("Tutorial TLS listener failed")
			exitCode = 1
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range servers {
		_ = server.Shutdown(shutdown)
	}
	return exitCode
}
