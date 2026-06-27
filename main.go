// Command tandoor-mcp is a Model Context Protocol server for the Tandoor
// Recipes REST API. By default it speaks MCP over stdio; setting
// TANDOOR_HTTP_ADDR switches it to the network transport (Streamable HTTP +
// SSE, with HTTP/2). It is configured entirely through environment variables
// (see tandoor.ConfigFromEnv, TANDOOR_IMAGE_DIR, and the TANDOOR_HTTP_ADDR /
// TANDOOR_MCP_TOKEN / TANDOOR_TLS_CERT / TANDOOR_TLS_KEY transport vars).
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/khr4/tandoor-mcp/internal/server"
	"github.com/khr4/tandoor-mcp/internal/tandoor"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("tandoor-mcp: ")

	cfg, err := tandoor.ConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	client, err := tandoor.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	opts, err := server.OptionsFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(client, opts)

	if addr := strings.TrimSpace(os.Getenv("TANDOOR_HTTP_ADDR")); addr != "" {
		err = server.Serve(ctx, srv, server.HTTPOptions{
			Addr:                      addr,
			Token:                     strings.TrimSpace(os.Getenv("TANDOOR_MCP_TOKEN")),
			TLSCert:                   strings.TrimSpace(os.Getenv("TANDOOR_TLS_CERT")),
			TLSKey:                    strings.TrimSpace(os.Getenv("TANDOOR_TLS_KEY")),
			AllowCleartextNonLoopback: strings.EqualFold(strings.TrimSpace(os.Getenv("TANDOOR_HTTP_ALLOW_CLEAR")), "true"),
			ReadyCheck:                server.TandoorReadyCheck(client),
		})
	} else {
		err = srv.Run(ctx, &mcp.StdioTransport{})
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("server stopped: %v", err)
	}
}
