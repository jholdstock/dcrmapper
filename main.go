package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/jholdstock/dcrmapper/crawler"
	"github.com/jholdstock/dcrmapper/server"
)

var (
	homeDir = dcrutil.AppDataDir("dcrmapper", false)
)

func run(ctx context.Context) error {

	// Parse CLI args.
	testnet := flag.Bool("testnet", false, "run on testnet")
	var listen, domain string
	flag.StringVar(&listen, "listen", "127.0.0.1:8111", "listen address:port")
	flag.StringVar(&domain, "domain", "localhost", "cookie domain")
	flag.Parse()

	params := chaincfg.MainNetParams()
	if *testnet {
		params = chaincfg.TestNet3Params()
	}

	mgr, err := crawler.New(homeDir, params, seedIPs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize crawler: %v\n", err)
		return err
	}

	// Waitgroup for services to signal when they have shutdown cleanly.
	var wg sync.WaitGroup

	mgr.Start(ctx, &wg)

	// Start HTTP server
	err = server.Start(ctx, listen, domain, mgr, shutdownRequestChannel, &wg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize server: %v\n", err)
		requestShutdown()
		wg.Wait()
	}

	// Wait for shutdown tasks to complete before running deferred tasks and
	// returning.
	wg.Wait()

	return ctx.Err()
}

func main() {
	// Create a context that is cancelled when a shutdown request is received
	// through an interrupt signal.
	ctx := withShutdownCancel(context.Background())
	go shutdownListener()

	// Run until error is returned, or shutdown is requested.
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
}
