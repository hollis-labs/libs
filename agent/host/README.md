# agenthost

The server's side of the agent-host wire (`WIRE.md`), for an application
that has no agent host of its own. It runs the model, keeps one running thread per owner,
and relays each tool call to the page that declared it (`@hollis-labs/ade-agent`).

```go
store, err := host.OpenSQLite(ctx, "data/agent.db") // or host.NewSQLite(ctx, appDB)
if err != nil { return err }
host := host.New(host.Config{
	Model:   "claude-sonnet-5-5",
	BaseURL: os.Getenv("MODEL_BASE_URL"),  // Anthropic-compatible: <base>/v1/messages
	KeyFile: os.Getenv("MODEL_KEY_FILE"),  // read per request; never reaches the page
	System:  "You are the tutor in a chess app. …",
}, store)
defer host.Close()

owner := func(r *http.Request) (string, error) { return myauth.UserID(r) } // your auth
h := host.Handler("/api/agent", owner)
mux.Handle("/api/agent", h)
mux.Handle("/api/agent/", h)
```

Levels: `read` and `act` run at once, `write` asks (once, session, always; always is kept),
`confirm` asks every time, no level is `write`. `host.AddServerTools` is the seam for tools
the server runs itself. Compaction summarises a long thread for the model (`CompactAt`).
