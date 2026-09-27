package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"journal/web"
)

func main() {
	cfg, err := web.ConfigFrom(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := web.Run(ctx, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
