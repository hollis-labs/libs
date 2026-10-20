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

The verifier requires `exp` (a missing or zero `exp` is `ErrInvalidToken`), so "no expiry" is expressed as a very distant `exp` (about ten years). Nothing else about verification changes: signature, `iss`, `aud`, scope and the deny-list apply as for any JWT.

Revocation is the deny-list: put the token's `jti` (or its `sub`) in the issuer's list. A consumer rejects the token after its next successful poll (default interval 30s). A failed poll keeps the previous list, so while the issuer's deny-list is unreachable a revoked token stays valid; with a ten-year `exp` there is no expiry to bound that, so monitor the poll (the `DenyListPoller` logs each failure). Because the token outlives every poll window, the issuer must keep the entry on the list until the token's own `exp`. The wire format is the one below; `service_test.go` holds the vectors (valid far-future token, still valid years later, rejected without `exp` (zero and absent), revoked by `jti` and by `sub`, scope and wildcard behaviour through the guard).

## Deny-list wire format

`GET` returns `{"jti":[...],"sub":[...],"sid":[...]}` with an `ETag`; the poller sends `If-None-Match` every 30s and keeps the last list on 304 or failure.

## Development

`go test -race ./...`, `golangci-lint run`. Run `lefthook install` once per clone.
