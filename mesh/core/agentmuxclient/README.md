# Legacy Agent Mux client

`github.com/hollis-labs/substrate/mesh/agentmuxclient` preserves the historical
`agentmux` Go package and HTTP protocol. Its complete source history is imported
into mesh; the former repository's versions are not module tags here.

```sh
go get github.com/hollis-labs/substrate/mesh@v0.2.0
```

```go
import agentmux "github.com/hollis-labs/substrate/mesh/agentmuxclient"
```

The package retains its default address `unix:~/.agent-mux/run/muxd.sock`,
legacy event fields (`ID`, `Event`), API errors, session/catalog/checkpoint
operations, broker endpoints, and messaging adapters. Its messaging dependency
is now the sibling mesh/messaging package. Consumer adoption is separate.

This is a preserved legacy client, not an alias for mesh/tetherclient. It does
not discover or attach Tether credentials, adopt newer endpoint contracts,
or turn asserted addresses into authority. It requires a server implementing
the legacy protocol; importing it does not establish compatibility with current
Tether daemons.

New integrations use [mesh/tetherclient](../tetherclient/README.md).
Follow its [migration guide](../tetherclient/MIGRATION.md) to account explicitly
for renamed event fields, socket defaults, endpoint changes, identity and
credentials. The existing Tether client is unchanged by this import.

Tests use private httptest servers and in-memory messaging; no daemon or provider
is needed.
