package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Liapoldus/plugin-sdk/tests/support/process"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Liapoldus/plugin-sdk/infrastructure"
)

type readyLine struct {
	Enabled   bool   `json:"enabled"`
	HealthURL string `json:"healthURL,omitempty"`
}

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "default":
		// A plugin that does not select NewLoopbackHealthServer opens no plaintext
		// SDK listener. This fixture deliberately uses no implicit profile default.
		process.Must(json.NewEncoder(os.Stdout).Encode(readyLine{Enabled: false}))
		waitForSignal()
	case "enabled":
		if err := serveEnabled(); err != nil {
			fmt.Fprintf(os.Stderr, "loopback fixture failed: %v\n", err)
			os.Exit(1)
		}
	case "remote":
		contract, err := infrastructure.LoadHTTPContract()
		if err != nil {
			os.Exit(1)
		}
		server, err := infrastructure.NewLoopbackHealthServer(contract, log.New(os.Stderr, "", 0))
		if err != nil {
			os.Exit(1)
		}
		if _, err = server.Listen("0.0.0.0:0"); err == nil {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "loopback bind rejected")
		os.Exit(2)
	default:
		os.Exit(2)
	}
}

func serveEnabled() error {
	contract, err := infrastructure.LoadHTTPContract()
	if err != nil {
		return err
	}
	server, err := infrastructure.NewLoopbackHealthServer(contract, log.New(os.Stderr, "", 0))
	if err != nil {
		return err
	}
	listener, err := server.Listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { process.Serve(server.Serve(listener)) }()
	if err := json.NewEncoder(os.Stdout).Encode(readyLine{
		Enabled:   true,
		HealthURL: "http://" + listener.Addr().String(),
	}); err != nil {
		return err
	}
	waitForSignal()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.GracefulShutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func waitForSignal() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}
