package edge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Serve serves h on ln until ctx ends, then drains in-flight requests for a
// few seconds. It returns nil after a clean shutdown.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
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
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-failed; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
