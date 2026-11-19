// approval-card serves a Gatecrasher-authored card through MCP Apps over stdio.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/ui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed app.html
var page string

//go:embed app.js
var script string

const resourceURI = "ui://gatecrasher/approval-card"

func newServer(card map[string]any, gateOrigin, intakeOrigin string) (*server.Server, error) {
	if card["version"] != "gatecrasher.card/v1" {
		return nil, fmt.Errorf("expected gatecrasher.card/v1")
	}
	switch card["type"] {
	case "approve", "choose", "answer", "request_secret":
	default:
		return nil, fmt.Errorf("unsupported card type")
	}
	// Origins are trusted operator configuration, never supplied by the model/card.
	config, err := json.Marshal(map[string]string{"gateOrigin": gateOrigin, "intakeOrigin": intakeOrigin})
	if err != nil {
		return nil, err
	}
	html := strings.ReplaceAll(page, "/*CONFIG*/", string(config))
	html = strings.ReplaceAll(html, "/*SCRIPT*/", script)
	s := server.NewServer("gatecrasher-card", "0.1.0", server.WithCapabilities(ui.Capabilities()), server.WithToolMiddleware(server.StrictArgs()))
	if err = ui.Register(s, ui.Resource{URI: resourceURI, Name: "Gatecrasher approval", HTML: html, ConnectDomains: []string{gateOrigin, intakeOrigin}}); err != nil {
		return nil, err
	}
	tool, err := ui.Attach(server.Tool{
		Name: "show_approval_card", Description: "Show the configured Gatecrasher approval card. Decisions and secret entry happen in the client UI.",
		InputSchema: server.EmptyObjectSchema(), ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false,
		Handler: func(context.Context, map[string]any) (any, error) { return map[string]any{"card": card}, nil },
	}, resourceURI)
	if err != nil {
		return nil, err
	}
	s.RegisterTool(tool)
	return s, nil
}

func main() {
	cardFile := flag.String("card", "card.json", "Gatecrasher /card response file (no credentials)")
	gate := flag.String("gate-origin", "https://gate.example", "trusted Gatecrasher origin")
	intake := flag.String("intake-origin", "https://intake.example", "trusted secret intake origin")
	flag.Parse()
	b, err := os.ReadFile(*cardFile)
	if err != nil {
		fail()
	}
	var card map[string]any
	if json.Unmarshal(b, &card) != nil {
		fail()
	}
	s, err := newServer(card, *gate, *intake)
	if err != nil {
		fail()
	}
	if s.SDKServer().Run(context.Background(), &mcp.StdioTransport{}) != nil {
		fail()
	}
}

// Do not echo card data, API bodies, URLs with capabilities, or credentials.
func fail() { fmt.Fprintln(os.Stderr, "approval-card: configuration or transport failed"); os.Exit(1) }
