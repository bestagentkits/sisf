// Command hello-app is the sample Zone 1 application of a factory
// deployment: the smallest app that has a health check, metrics and a data
// volume to write to. A factory publishes, observes and rolls back an app
// like this one.
//
// Settings come from the environment:
//
//	HELLO_ADDR      listen address, default 127.0.0.1:7461
//	HELLO_DATA_DIR  writable data volume, default ./data
//
// Exit status: 0 after a clean shutdown, 1 on failure.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Getenv)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hello-app:", err)
		os.Exit(1)
	}
}

// run serves the application until ctx ends.
func run(ctx context.Context, getenv func(string) string) error {
	addr := valueOr(getenv("HELLO_ADDR"), "127.0.0.1:7461")
	a, err := newApp(valueOr(getenv("HELLO_DATA_DIR"), "data"))
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           a.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
