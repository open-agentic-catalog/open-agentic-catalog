// Deployment coordination is an operator tool, never part of catalogd startup.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

type options struct {
	context, namespace, release, chart, values, helm, kubectl string
	timeout                                                   time.Duration
	check                                                     bool
}

type command func(context.Context, string, ...string) ([]byte, error)

// Tool output can contain credentials. Admit only bounded stdout internally;
// errors emitted to the operator are fixed stages, never dependency text.
type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 8<<20 {
		b.overflow = true
		return n, nil
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func execute(ctx context.Context, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...) // #nosec G204 G702 -- operator-selected local tools and argument vectors; no shell or remote input.
	var output boundedOutput
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &output, io.Discard, 2*time.Second
	if err := cmd.Run(); err != nil || output.overflow {
		return nil, errors.New("deployment tool failed")
	}
	return output.Bytes(), nil
}

func (o options) validate() error {
	name := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	if o.context == "" || len(o.context) > 253 || !name.MatchString(o.namespace) || len(o.namespace) > 63 || !name.MatchString(o.release) || len(o.release) > 53 || o.chart == "" || o.values == "" || o.helm == "" || o.kubectl == "" || o.timeout < time.Second || o.timeout > time.Hour {
		return errors.New("select explicit context, namespace, release, local chart, values and a timeout between 1s and 1h")
	}
	return nil
}

// Freeze local artifacts before any cluster mutation; no repository download,
// implicit values reuse or dependency update is allowed by this entry point.
func snapshot(ctx context.Context, o options, run command, directory string) (options, error) {
	info, err := os.Stat(o.chart)
	if err != nil {
		return o, errors.New("local chart unavailable")
	}
	if info.IsDir() {
		if _, err := run(ctx, o.helm, "package", o.chart, "--destination", directory); err != nil {
			return o, errors.New("chart packaging failed")
		}
		paths, err := filepath.Glob(filepath.Join(directory, "*.tgz"))
		if err != nil || len(paths) != 1 {
			return o, errors.New("chart package missing")
		}
		o.chart = paths[0]
	} else {
		if err := copyInput(o.chart, filepath.Join(directory, "chart.tgz"), 32<<20); err != nil {
			return o, err
		}
		o.chart = filepath.Join(directory, "chart.tgz")
	}
	if err := copyInput(o.values, filepath.Join(directory, "values.yaml"), 1<<20); err != nil {
		return o, err
	}
	o.values = filepath.Join(directory, "values.yaml")
	return o, nil
}
func copyInput(source, destination string, limit int64) error {
	f, err := os.Open(source) // #nosec G304 -- explicitly selected local operator input.
	if err != nil {
		return errors.New("deployment input unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("deployment input must be a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return errors.New("deployment input read failed")
	}
	if err := os.WriteFile(destination, raw, 0o600); err != nil {
		return errors.New("deployment input snapshot write failed")
	}
	return nil
}

func main() {
	var o options
	flag.StringVar(&o.context, "context", "", "explicit Kubernetes context")
	flag.StringVar(&o.namespace, "namespace", "", "existing namespace")
	flag.StringVar(&o.release, "release", "", "Helm release")
	flag.StringVar(&o.chart, "chart", "", "local chart directory or archive")
	flag.StringVar(&o.values, "values", "", "complete target values file")
	flag.StringVar(&o.helm, "helm", "helm", "Helm executable")
	flag.StringVar(&o.kubectl, "kubectl", "kubectl", "kubectl executable")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Minute, "bound for each drain/deployment/readiness phase")
	flag.BoolVar(&o.check, "check", false, "validate and dry-run only; do not drain or deploy")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Catalog deployment: unexpected positional arguments")
		os.Exit(2)
	}
	if err := entry(o); err != nil {
		fmt.Fprintln(os.Stderr, "Catalog deployment:", err)
		os.Exit(1)
	}
}
func entry(o options) error {
	if err := o.validate(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 4*o.timeout+2*time.Minute)
	defer cancel()
	directory, err := os.MkdirTemp("", "catalog-deploy-")
	if err != nil {
		return errors.New("private deployment directory unavailable")
	}
	defer func() { _ = os.RemoveAll(directory) }()
	prepare, preparedDone := context.WithTimeout(ctx, 2*time.Minute)
	prepared, err := snapshot(prepare, o, execute, directory)
	preparedDone()
	if err != nil {
		return err
	}
	return deploy(ctx, prepared, execute, func(stage string) { fmt.Println("Catalog deployment:", stage) })
}
