# go-auth

Shared bearer auth for the Hollis mesh. One wire contract (`Authorization: Bearer <token>`, scope strings `<app>.<verb>`, a `Principal` in request context), one middleware, pluggable verifiers. Tether issues Ed25519 JWTs; apps verify them offline against its JWKS.

```
go get github.com/hollis-labs/libs/security/auth   # package name: auth
```

## HTTP app

```go
mode, _ := auth.ModeFromEnv("tesseract", auth.ModeEnforce) // HOLLIS_AUTH_TESSERACT, then HOLLIS_AUTH
if err := auth.ValidateBind(addr, mode); err != nil { log.Fatal(err) }
dl := auth.NewDenyList()
go (&auth.DenyListPoller{URL: issuer + "/auth/denylist", List: dl}).Run(ctx)
jv, _ := auth.NewJWTVerifier(auth.JWTConfig{Issuer: issuer, Audience: "tesseract", JWKSURL: issuer + "/.well-known/jwks.json",
	BootstrapKeyFile: "/etc/hollis/issuer.pub.pem", DenyList: dl})
go jv.Run(ctx) // periodic JWKS refresh; unknown kid also triggers a rate-limited refetch
keys := auth.NewAPIKeyVerifier(auth.APIKey{Hash: rootKeyHash, ID: "svc/root", Scopes: []string{"tesseract.*"}})
scopes := auth.NewScopeTable().Route("GET", "/notes/{id}", "tesseract.read").Route("POST", "/notes", "tesseract.write")
g, _ := auth.New(auth.Config{Mode: mode, Verifier: auth.Chain(jv, keys), Scopes: scopes, Audit: audit})
http.ListenAndServe(addr, g.Middleware(mux)) // handlers: p, _ := auth.FromContext(r.Context()); health: g.Status()
```

## MCP server

```go
// HTTP transport: wrap the MCP handler with g.Middleware (above) so the principal reaches tool handlers.
// stdio transport: stamp the principal once at startup from the token file / env your launcher gave you.
scopes.Tool("note_create", "tesseract.write").Tool("note_get", "tesseract.read")
func callTool(ctx context.Context, name string, args json.RawMessage) (any, error) {
	if err := g.AuthorizeTool(ctx, name); err != nil { // ErrUnauthenticated | ErrForbidden (enforce only)
		return nil, err
	}
	return dispatch(ctx, name, args)
}
```

## Modes

| mode | behavior |
| --- | --- |
| `off` | never rejects. A valid token is verified and stamped; otherwise the synthetic principal `operator(off-mode)` (unverified, scope `*`) is stamped and an audit event with `authentication=off-mode` is emitted. |
| `permissive` | alias `observe`. Verifies, never rejects, logs and audits "would deny" with the required scope. |
| `enforce` | 401 missing/invalid, 503 verifier unavailable, 403 missing scope. Routes and tools absent from the table are denied (fail closed) unless `ScopeTable.Default` names a scope or `AllowUnmapped` is set; a nil table means authentication only. |

`ValidateBind(addr, mode)` refuses `off` and `permissive` unless the listener is loopback, tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48, `*.ts.net`) or a unix socket. Wildcard binds (`:8080`, `0.0.0.0`) count as non-local.

## Verifiers

- `JWTVerifier`: EdDSA only (any other `alg`, including `none`, is rejected); checks signature, `exp`/`nbf` (30s leeway), `iss` (if configured), `aud` (a single string equal to the app id; an aud array is rejected), `sub`, and the deny-list (`jti`, `sub`, `sid`). `kid` rotation: unknown kid triggers a refetch (at most one per `MinRefresh`), and a refresh replaces the key set so rotated-out keys stop working. `BootstrapKeyFile` (JWKS JSON or PEM public key) is used until the first successful fetch. Issuer down with no key cached yields `ErrUnavailable` (503), not 401.
- `APIKeyVerifier`: static SHA-256-hashed keys (`HashKey`), same scope strings. Interim backend for root service credentials.
- `Chain(v...)`: first success wins.

Scope grants match exactly, `*`, or `<app>.*`. Claims read: `iss sub aud scope exp nbf jti kind sid par`.

## Posture dial

Each app reads its own dial with `ModeFromEnv("<app>", def)`: `HOLLIS_AUTH_<APP>` first, then the global `HOLLIS_AUTH`, then `def`. The per-app variable wins over the global one. The app name is upper-cased and every non-alphanumeric becomes `_` (`my-app` reads `HOLLIS_AUTH_MY_APP`). Accepted values are `off`, `permissive` (alias `observe`) and `enforce`; anything else is an error. Dev default is `off`; see Modes for the behaviour of each.

## Service tokens (dev)

A service token is a normal Tether JWT with `kind=service`, a scope list of `<app>.<verb>` strings and a `jti`. It carries no `sid`. It is meant to be configured once as a static bearer and to stay valid until revoked.

The verifier requires `exp` (a missing or zero `exp` is `ErrInvalidToken`), so a service token is effectively non-expiring: it carries a very distant `exp` (about ten years) and is ended by revocation through the deny-list, not by time. This is not a new lifetime policy; it is how "no expiry" is expressed to a verifier that requires `exp`. Nothing else about verification changes: signature, `iss`, `aud`, scope and the deny-list apply as for any JWT.

Revocation is the deny-list: put the token's `jti` (or its `sub`) in the issuer's list. Revocation takes effect within one poll interval (default 30s) after a successful poll. If the issuer or its deny-list is unreachable, the last list stays in force (stale), so a revoked token keeps being accepted for as long as the outage lasts; with a ten-year `exp` there is no expiry to bound that, so monitor the poll (the `DenyListPoller` logs each failure). Because the token outlives every poll window, the issuer must keep the entry on the list until the token's own `exp`. The wire format is the one below; `service_test.go` holds the vectors (valid far-future token, still valid years later, rejected without `exp` (zero and absent), revoked by `jti` and by `sub`, scope and wildcard behaviour through the guard).

## Keeping a short-lived token alive (refresher)

Agent and session JWTs live 15 minutes. `Refresher` keeps one valid for as long as the process runs, so a consumer never hand-rolls renewal:

```go
m := &auth.HTTPMinter{URL: hub + "/auth/token", Body: map[string]any{"audience": "torque", "scopes": []string{"torque.read"}, "session_id": sid},
	Credential: func() (string, error) { return tokenfile.Read(deviceTokenPath) }, // re-read on every mint
	Header:     http.Header{"Tether-Protocol": {"1"}}}
r, _ := auth.NewRefresher(auth.RefresherConfig{Mint: m.Mint})
go r.Run(ctx)               // optional: renew ahead of demand
tok, err := r.Token(ctx)    // per request; never expired, no restart needed
```

Contract:
- Renews at 2/3 of the token's life (`Fraction`, default `DefaultRefreshFraction`): 10 minutes for a 15 minute token.
- `Token` is safe for concurrent use and renews lazily. At most one mint runs at a time; a caller that finds the token due while another caller is renewing gets the still-valid token without waiting.
- If the issuer is unreachable at renewal time, the held token keeps being served until it is within `Skew` (5s) of expiry, and mints retry with exponential backoff (`RetryMin` 2s to `RetryMax` 60s). After that `Token` returns `ErrNoToken` wrapping the mint error. It recovers by itself when the issuer returns.
- A reply is judged against the clock when it arrives: a token already expired, or inside `Skew` of expiry, is refused as a failed mint, so the token already held is kept and the retry backoff applies.
- `Token` honors its context: a caller waiting behind a stalled mint returns `ctx.Err()` as soon as its context is done.
- `OnRefresh` reports each attempt's result and new expiry; the token is never passed to callbacks, logged or put in an error.
- `HTTPMinter` posts JSON with the parent credential as a bearer and accepts `{"token", "expires_at"|"ttl_seconds"}` (the shape of Tether's `POST /auth/token`); it falls back to the JWT's own `exp`. It refuses cross-origin redirects. Errors carry the HTTP status at most: never the issuer's reply body (an endpoint may echo the Authorization header), the URL, or the credential.
- Service tokens (below) do not need a refresher.

## Reloading config without a restart

Verifier and posture configuration can change while the app serves requests. Three small pieces, one mechanism:

```go
g, _ := auth.New(auth.Config{Mode: mode, Verifier: jv, Scopes: scopes, Addr: addr})
load := func(ctx context.Context) error {                   // all-or-nothing: build everything, then install once
	cfg, err := readConfig(path)                            // your format (YAML, JSON, env file)
	if err != nil { return err }                            // previous config stays in effect
	jv, err := auth.NewJWTVerifier(cfg.JWT)
	if err != nil { return err }
	return g.Apply(cfg.Mode, jv)                            // validates, then swaps mode and verifier together or not at all
}
_ = load(ctx)                                               // startup load
go (&auth.Reloader{Path: path, Load: load}).Run(ctx)
```

If `Load` builds anything with a lifetime of its own (a JWKS refresh loop, a deny-list poller), start it for the new configuration only after `Apply` succeeded and stop the old one afterwards; `CheckApply(mode, v)` answers "would this be accepted" without changing anything.

- `Reloader` calls `Load` when the file's content changes (checked every 2s; a change must be seen unchanged on two consecutive checks, so a half-written file is not loaded), on `SIGHUP`, and on `Trigger()`. A touch with identical content does nothing. `Load` is not called for the content present when `Run` starts. The file is fingerprinted before `Load` runs, so an edit made while `Load` is running is loaded again afterwards, never marked applied unseen.
- A `Load` error (or panic) is logged and passed to `OnResult`; the running configuration is untouched. A file that failed to load is not retried until its content changes again, or a signal or `Trigger` forces it. A file missing mid-replace is ignored.
- `Guard.Apply(mode, verifier)` moves the posture dial and the verifier live as one atomic step (`SetMode(m)` is `Apply(m, nil)`). A request takes its mode and verifier once, on entry, before any verification, and keeps that pair to the end even if `Apply` runs meanwhile. It refuses, leaving both unchanged, a mode `ParseMode` does not accept, `enforce` with no verifier, and, when `Config.Addr` is set, any mode `ValidateBind` rejects for that address (a reload cannot loosen auth on a non-local listener). Modes are stored in canonical form, so `ENFORCE` and ` observe ` mean what they say; a mode the guard does not recognize is treated as `enforce`.
- `Switch` is a swappable `Verifier` for code that does not use a `Guard`; with no verifier it answers `ErrUnavailable` (503 under enforce), never a pass.
- Rotated JWKS keys already refresh on their own (`Run`, or an unknown `kid`); reload is for changing the verifier's configuration itself (issuer, audience, JWKS URL, bootstrap key, deny-list source).

## Deny-list wire format

`GET` returns `{"jti":[...],"sub":[...],"sid":[...]}` with an `ETag`; the poller sends `If-None-Match` every 30s and keeps the last list on 304 or failure.

## Development

`go test -race ./...`, `golangci-lint run`. Run `lefthook install` once per clone.
