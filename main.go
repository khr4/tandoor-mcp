// Command tandoor-mcp is a Model Context Protocol server for the Tandoor
// Recipes REST API. It speaks MCP over stdio and is configured entirely through
// environment variables (see ConfigFromEnv).
package main

import (
	"context"
	"log"

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
	if err := server.New(client).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
