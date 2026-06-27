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
	"net/http"
	"net/url"
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
	opts := server.Options{ImageDir: os.Getenv("TANDOOR_IMAGE_DIR")}

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
			ReadyCheck: func(ctx context.Context) error {
				q := url.Values{"page_size": []string{"1"}}
				_, err := client.Do(ctx, http.MethodGet, "recipe/", q, nil)
				return err
			},
		})
	} else {
		err = srv.Run(ctx, &mcp.StdioTransport{})
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("server stopped: %v", err)
	}
}
