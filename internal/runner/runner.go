package runner

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/jessekalil/rds-bridge/internal/awsiam"
	"github.com/jessekalil/rds-bridge/internal/config"
	"github.com/jessekalil/rds-bridge/internal/proc"
	"github.com/jessekalil/rds-bridge/internal/proxy"
	"github.com/jessekalil/rds-bridge/internal/tunnel"
)

const readyTimeout = 30 * time.Second

// Run brings up the tunnel and proxy for a target and blocks until the process
// receives SIGINT/SIGTERM, then shuts everything down (tunnel child included).
func Run(t *config.Target) error {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	tag := func(prefix string) func(string, ...any) {
		return func(format string, args ...any) {
			logger.Printf("[%s] "+format, append([]any{prefix}, args...)...)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), proc.ShutdownSignals()...)
	defer stop()

	sup := tunnel.New(t, tag("tunnel"))
	go sup.Run(ctx)

	tag("tunnel")("waiting for local port %d", t.SSM.LocalPort)
	if err := tunnel.WaitReady(ctx, t.SSM.LocalPort, readyTimeout); err != nil {
		return fmt.Errorf("tunnel not ready: %w", err)
	}
	tag("tunnel")("ready")

	auth := awsiam.New(t.AWSRegion, t.IAM.Profile, t.IAM.TokenHost, t.IAM.TokenPort, t.IAM.User)
	user, err := auth.User(ctx)
	if err != nil {
		return fmt.Errorf("resolve iam user: %w", err)
	}
	tag("iam")("authenticating as %s", user)

	tlsCfg, err := proxy.SelfSignedTLS()
	if err != nil {
		return err
	}

	px := proxy.New(t.ListenPort, t.Local, t.SSM.LocalPort, auth, tlsCfg, tag("proxy"))
	errc := make(chan error, 1)
	go func() { errc <- px.Listen(ctx) }()
	tag("proxy")("ready — app can connect to 127.0.0.1:%d (choose database via dbname)", t.ListenPort)

	select {
	case <-ctx.Done():
		tag("")("shutting down")
		stop()
		// Give the tunnel goroutine a moment to kill its child group.
		time.Sleep(500 * time.Millisecond)
		return nil
	case err := <-errc:
		if err != nil {
			stop()
		}
		return err
	}
}
