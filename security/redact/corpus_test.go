package redact_test

import (
	"strings"
	"testing"

	"github.com/hollis-labs/libs/security/redact"
)

// The conformance corpus. Every value is invented: credential-shaped values are
// assembled at run time so the source holds no complete fake credential for a
// scanner to mistake for one. Expect "redact": none of Secret may survive and every
// Keep must. Expect "keep": the text must come back unchanged. Both must be
// idempotent. Cases that need an option or are known gaps are listed in the tests below.

type Case struct {
	ID     string
	Class  string
	Input  string
	Expect string
	Secret []string
	Keep   []string
	Note   string
}

func j(parts ...string) string { return strings.Join(parts, "") }

var (
	skKey    = j("sk-", "Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4Z")
	skAnt    = j("sk-", "ant-api03-Zx9Yw8Vu7Ts6Rq5Pn4Om3Lk2Ji1Hg0FeDcBa_XyWvUtSrQp-AAAA")
	skProj   = j("sk-", "proj-Qw3Er5Ty7Ui9Op1As2Df4Gh6Jk8Lz0XcVbNm")
	ghp      = j("ghp", "_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R")
	ghpat    = j("git", "hub_pat_11ABCDEFG0aBcDeFgHiJk_lMnOpQrStUvWxYz0123456789aBcDeFgHiJkLmNoPqRsTuVwXyZ")
	xoxb     = j("xox", "b-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx")
	akia     = j("AKI", "AJ3Q7ZZ5T2XN4WQ6B")
	asia     = j("ASI", "AX4Q7ZZ5T2XN4WQ6B")
	glpat    = j("glp", "at-Ab12Cd34Ef56Gh78Ij90")
	aiza     = j("AIz", "aSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q")
	npmTok   = j("npm", "_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78")
	sg       = j("SG.", "Ab12Cd34Ef56Gh78Ij90Kl.Mn34Op56Qr78St90Uv12Wx34Yz56Ab78Cd90Ef12Gh34Ij56")
	stripeL  = j("sk_", "live_51Hx9aBcDeFgHiJkLmNoPqRs")
	stripeR  = j("rk_", "live_51Hx9aBcDeFgHiJkLmNoPqRs")
	stripeT  = j("sk_", "test_51Hx9aBcDeFgHiJkLmNoPqRs")
	jwt      = j("eyJ", "hbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0LXVzZXIiLCJuYW1lIjoiRXhhbXBsZSJ9.c2lnbmF0dXJlLXN5bnRoZXRpYy1vbmx5")
	awsSec   = j("q7Z", "x3Lm9Pv2Nc5Rt8Ws1Ab4Dy6Fg0Hj2KlMnOpQr")
	pemBody  = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7SYNTHETICfakekeyAAAA\nBBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJKKKKLLLLMMMMNNNNOOOOPPPPQQQQRRRRSSSSTTTT"
	ghsTok   = j("ghs_", "Z9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3i2")
	ghsShort = j("ghs_", "Z9y8X7w6V5u4T3s2R1q0P9o8N7m6")
)

func R(id, class, in string, secret []string, keep ...string) Case {
	return Case{ID: id, Class: class, Input: in, Expect: "redact", Secret: secret, Keep: keep}
}
func K(id, class, in string) Case     { return Case{ID: id, Class: class, Input: in, Expect: "keep"} }
func (c Case) withNote(n string) Case { c.Note = n; return c }

func All() []Case {
	s := func(v ...string) []string { return v }
	return []Case{
		// ---- PK: provider key shapes
		R("pk01", "PK", "key is "+skKey+" ok", s(skKey), "key is", "ok"),
		R("pk02", "PK", "ANTHROPIC "+skAnt+" end", s(skAnt), "end"),
		R("pk03", "PK", "using "+skProj+" for proj", s(skProj), "for proj"),
		R("pk04", "PK", "token "+ghp+" used", s(ghp), "used"),
		R("pk05", "PK", "pat "+ghpat+" used", s(ghpat), "used"),
		R("pk06", "PK", "slack "+xoxb+" used", s(xoxb), "used"),
		R("pk07", "PK", "aws id "+akia+" used", s(akia), "used"),
		R("pk08", "PK", "aws temp id "+asia+" used", s(asia), "used"),
		R("pk09", "PK", "gitlab "+glpat+" used", s(glpat), "used"),
		R("pk10", "PK", "google "+aiza+" used", s(aiza), "used"),
		R("pk11", "PK", "npm "+npmTok+" used", s(npmTok), "used"),
		R("pk12", "PK", "sendgrid "+sg+" used", s(sg), "used"),
		R("pk13", "PK", "stripe "+stripeL+" used", s(stripeL), "used"),
		R("pk14", "PK", "stripe restricted "+stripeR+" used", s(stripeR), "used"),
		R("pk15", "PK", "stripe test "+stripeT+" used", s(stripeT), "used"),
		R("pk16", "PK", "jwt "+jwt+" used", s(jwt), "used"),
		R("pk17", "PK", "ghs "+"ghs_"+"Z9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3i2"+" used", s(ghsTok), "used"),
		// ---- HDR: auth headers
		R("hd01", "HDR", "Authorization: Bearer abcDEF123456.tok-synthetic_value done", s("abcDEF123456.tok-synthetic_value"), "done"),
		R("hd02", "HDR", "Authorization: Basic dXNlcjpodW50ZXIyc3ludGg= done", s("dXNlcjpodW50ZXIyc3ludGg="), "done"),
		R("hd03", "HDR", `curl -H "Authorization: Bearer abcDEF123456.tok-synthetic_value" https://api.example.com/v1`, s("abcDEF123456.tok-synthetic_value"), "https://api.example.com/v1"),
		R("hd04", "HDR", "X-API-Key: k_live_9f8e7d6c5b4a39281706 done", s("k_live_9f8e7d6c5b4a39281706"), "done"),
		R("hd05", "HDR", "Proxy-Authorization: Basic cHJveHk6c3ludGhldGljcGFzcw== done", s("cHJveHk6c3ludGhldGljcGFzcw=="), "done"),
		R("hd06", "HDR", "x-auth-token: 7f3a9c1e5b2d4086a1c3e5f70912b4d6\nnext line stays", s("7f3a9c1e5b2d4086a1c3e5f70912b4d6"), "next line stays"),
		R("hd07", "HDR", `{"headers":{"authorization":"Bearer abcDEF123456.tok-synthetic_value","accept":"json"}}`, s("abcDEF123456.tok-synthetic_value"), `"accept":"json"`),
		R("hd08", "HDR", "Bearer short9", s("short9")).withNote("short but token-shaped bearer (has a digit)"),
		// ---- PEM
		R("pm01", "PEM", "before\n-----BEGIN RSA PRIVATE KEY-----\n"+pemBody+"\n-----END RSA PRIVATE KEY-----\nafter", s("MIIEvQIBADANBgkq", "BBBBCCCCDDDD"), "before", "after"),
		R("pm02", "PEM", "before\n-----BEGIN OPENSSH PRIVATE KEY-----\n"+pemBody+"\n-----END OPENSSH PRIVATE KEY-----\nafter", s("MIIEvQIBADANBgkq", "BBBBCCCCDDDD"), "before", "after"),
		R("pm03", "PEM", "log start\n-----BEGIN PRIVATE KEY-----\n"+pemBody+"\n", s("MIIEvQIBADANBgkq", "BBBBCCCCDDDD"), "log start").withNote("truncated: no END line (tail cut)"),
		R("pm04", "PEM", `{"key":"-----BEGIN EC PRIVATE KEY-----\n`+"MHcCAQEEIFakeFakeFakeFakeFakeFakeFake"+`\n-----END EC PRIVATE KEY-----\n","id":7}`, s("MHcCAQEEIFakeFake"), `"id":7`),
		R("pm05", "PEM", "-----BEGIN ENCRYPTED PRIVATE KEY-----\n"+pemBody+"\n-----END ENCRYPTED PRIVATE KEY-----", s("MIIEvQIBADANBgkq", "BBBBCCCCDDDD")),
		K("pm06", "FP", "-----BEGIN CERTIFICATE-----\nMIIDdzCCAl+gAwIBAgIEAgAAuTANBgkqhkiG9w0BAQUFADBaMQswCQYDVQQGEwJJ\n-----END CERTIFICATE-----"),
		K("pm07", "FP", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1synthetic1PublicKeyOnly0000000000000 user@host"),
		// ---- ASG: assignments
		R("as01", "ASG", "DATABASE_PASSWORD=Sup3rS3cretPw! next", s("Sup3rS3cretPw!"), "next"),
		R("as02", "ASG", `export STRIPE_SECRET_KEY="whsec_synthetic0123456789"`+"\necho done", s("whsec_synthetic0123456789"), "echo done"),
		R("as03", "ASG", `{"user":"bob","password":"hunter2-synthetic","ok":true}`, s("hunter2-synthetic"), `"user":"bob"`, `"ok":true`),
		R("as04", "ASG", `{"client_id":"keepme-id","client_secret":"abc123XYZ789synthetic"}`, s("abc123XYZ789synthetic"), "keepme-id"),
		R("as05", "ASG", "api_key: abcd1234efgh5678ijkl\nregion: us-east-1\n", s("abcd1234efgh5678ijkl"), "region: us-east-1"),
		R("as06", "ASG", "db:\n  password: \"pa ss word synthetic\"\n  host: db.internal\n", s("pa ss word synthetic"), "host: db.internal"),
		R("as07", "ASG", `level=info msg="login" user=bob token=tok_9f8e7d6c5b4a3 duration=12ms`, s("tok_9f8e7d6c5b4a3"), "duration=12ms", "user=bob"),
		R("as08", "ASG", "{'password': 'p4ss-synth', 'user': 'bob'}", s("p4ss-synth"), "'user': 'bob'"),
		R("as09", "ASG", "A=1\nSECRET_TOKEN=zzTopSynth99\nB=2\nPATH=/usr/bin\n", s("zzTopSynth99"), "A=1", "B=2", "PATH=/usr/bin"),
		R("as10", "ASG", "PGPASSWORD=pgsynth123 psql -h db", s("pgsynth123"), "psql -h db"),
		R("as11", "ASG", "MYSQL_PWD=mysynth456 mysqldump x", s("mysynth456"), "mysqldump x"),
		R("as12", "ASG", `config: {apiKey: "k3y-synth-0001", mode: fast}`, s("k3y-synth-0001"), "mode: fast"),
		R("as13", "ASG", "db.password = s3cr3t-synth!\ndb.user = app\n", s("s3cr3t-synth!"), "db.user = app"),
		R("as14", "ASG", "SECRET_KEY_BASE=0f9e8d7c6b5a4938271605f4e3d2c1b0 end", s("0f9e8d7c6b5a4938271605f4e3d2c1b0"), "end"),
		R("as15", "ASG", "data:\n  password: aHVudGVyMnN5bnRoZXRpYw==\n  user: Ym9i\n", s("aHVudGVyMnN5bnRoZXRpYw=="), "user: Ym9i"),
		R("as16", "ASG", "AWS_SESSION_TOKEN=FwoGZXIvYXdzEBYaDEXAMPLESYNTHETICtokenvalue0123456789ABCDEFGHIJ end", s("FwoGZXIvYXdzEBYaDEXAMPLESYNTHETIC"), "end"),
		// ---- CLI
		R("cl01", "CLI", "run --token abc123synthetic now", s("abc123synthetic"), "run", "now"),
		R("cl02", "CLI", "run --password=hunter2synth now", s("hunter2synth"), "now"),
		R("cl03", "CLI", "sshpass -p hunter2synth ssh host", s("hunter2synth"), "ssh host"),
		R("cl04", "CLI", "mysql -u root -pSynthPass99 db", s("SynthPass99"), "mysql -u root", " db"),
		R("cl05", "CLI", "curl -u admin:s3cr3tPassSynth https://x.example.com/a", s("s3cr3tPassSynth"), "https://x.example.com/a"),
		R("cl06", "CLI", "curl --user admin:s3cr3tPassSynth https://x.example.com/a", s("s3cr3tPassSynth"), "https://x.example.com/a"),
		R("cl07", "CLI", "tool --api-key sk_live_synthetic99887766 --verbose", s("sk_live_synthetic99887766"), "--verbose"),
		R("cl08", "CLI", "tool --client-secret=cs-synth-445566 --verbose", s("cs-synth-445566"), "--verbose"),
		R("cl09", "CLI", "psql \"host=db user=app password=pgSynth789 dbname=x\"", s("pgSynth789"), "dbname=x"),
		R("cl10", "CLI", "aws configure set aws_secret_access_key "+awsSec+" --profile p", s(awsSec), "--profile p"),
		// ---- URL
		R("ur01", "URL", "GET https://bob:hunter2synth@example.com/path ok", s("hunter2synth"), "example.com/path", "ok"),
		R("ur02", "URL", "GET https://example.com/cb?token=abc123synthetic&x=1 ok", s("abc123synthetic"), "x=1"),
		R("ur03", "URL", "GET https://example.com/cb?a=1&api_key=Zk9synthetic8877&b=2 ok", s("Zk9synthetic8877"), "a=1", "b=2"),
		R("ur04", "URL", "GET https://s3.example.com/f?X-Amz-Credential=AKIA0000%2F2026&X-Amz-Signature=9d8c7b6a5f4e3d2c1b0a99887766554433221100&X-Amz-Expires=60", s("9d8c7b6a5f4e3d2c1b0a99887766554433221100"), "X-Amz-Expires=60"),
		R("ur05", "URL", "git clone https://ghx_synthetictoken0001@github.com/o/r.git", s("ghx_synthetictoken0001"), "github.com/o/r.git").withNote("userinfo with token and no colon"),
		R("ur06", "URL", "git clone https://x-access-token:"+ghsShort+"@github.com/o/r.git", s(ghsShort), "github.com/o/r.git"),
		R("ur07", "URL", "redirect https://app.example.com/cb?access_token=ya29.synthetic0123456789&state=xyz", s("ya29.synthetic0123456789"), "state=xyz"),
		R("ur08", "URL", "wss://h.example.com/s?sig=aaBBccDD1122ee33ff44&room=7", s("aaBBccDD1122ee33ff44"), "room=7").withNote("generic 'sig' param name"),
		// ---- CONN
		R("cn01", "CONN", "dsn postgres://app:pgSynth123@db:5432/app?sslmode=disable end", s("pgSynth123"), "db:5432/app", "end"),
		R("cn02", "CONN", "dsn redis://:redisSynth456@cache:6379/0 end", s("redisSynth456"), "cache:6379/0", "end"),
		R("cn03", "CONN", "dsn amqp://guest:amqpSynth789@mq:5672/vhost end", s("amqpSynth789"), "mq:5672/vhost", "end"),
		R("cn04", "CONN", "dsn app:mySynth012@tcp(127.0.0.1:3306)/db?parseTime=true end", s("mySynth012"), "tcp(127.0.0.1:3306)/db", "end"),
		R("cn05", "CONN", "dsn mongodb+srv://u1:mongoSynth345@cluster0.example.net/db end", s("mongoSynth345"), "cluster0.example.net/db", "end"),
		R("cn06", "CONN", "Server=tcp:db.example.net,1433;User Id=sa;Password=SqlSynth345;Encrypt=true;", s("SqlSynth345"), "Encrypt=true", "db.example.net,1433"),
		// ---- ENC: encoded / evasion
		R("en01", "ENC", "body=user%3Dbob%26password%3Dhunter2synth%26x%3D1", s("hunter2synth"), "user%3Dbob").withNote("url-encoded KEY=value"),
		R("en02", "ENC", "cb?token=abc%2Fdef%2Bghi%3D%3D&z=1", s("abc%2Fdef%2Bghi%3D%3D"), "z=1"),
		R("en03", "ENC", `{"log":"payload={\"password\":\"hunter2synth\"}"}`, s("hunter2synth"), `{"log":`).withNote("JSON-escaped nested JSON"),
		R("en04", "ENC", "blob c2VjcmV0LXNlcnZpY2UtYWNjb3VudC1rZXktc3ludGhldGljLTEyMzQ1Njc4OTA9 end", s("c2VjcmV0LXNlcnZpY2UtYWNjb3VudC1rZXktc3ludGhldGljLTEyMzQ1Njc4OTA9"), "end").withNote("bare base64, no label: entropy only"),
		R("en05", "ENC", "key is sk-Ab3dEf6hIj9k​Lm2nOp5qRs8tUv1wXy4Z end", s("Lm2nOp5qRs8tUv1wXy4Z"), "end").withNote("zero-width space inside token"),
		R("en06", "ENC", "ｐａｓｓｗｏｒｄ=hunter2synth end", s("hunter2synth"), "end").withNote("fullwidth 'password'"),
		// ---- CK: cookies
		R("ck01", "CK", "Cookie: session=abc123synthetic; theme=dark\nHost: x", s("abc123synthetic"), "Host: x"),
		R("ck02", "CK", "Set-Cookie: sid=9f8e7d6c5b4a3210; Path=/; HttpOnly; Secure\nnext", s("9f8e7d6c5b4a3210"), "next"),
		R("ck03", "CK", `{"headers":{"cookie":"sessionid=zz99synthetic00; csrftoken=q1"},"ok":1}`, s("zz99synthetic00"), `"ok":1`),
		// ---- AWS bare secret
		R("aw01", "AWS", "the secret was "+awsSec+" in the log", s(awsSec), "in the log").withNote("40-char, no label"),
		R("aw02", "AWS", "aws_secret_access_key = "+awsSec+"\nregion = us-east-1", s(awsSec), "region = us-east-1"),
		R("aw03", "AWS", "AWS_SECRET_ACCESS_KEY="+awsSec+" AWS_REGION=us-east-1", s(awsSec), "AWS_REGION=us-east-1"),
		// ---- PII (expected: redact)
		// ---- FP: bait that must be left alone
		K("fp01", "FP", "This endpoint accepts a Bearer JWT only."),
		K("fp02", "FP", "set X-API-Key header on the tunnel"),
		K("fp03", "FP", "tokenizer settings loaded; the author used a keyboard"),
		K("fp04", "FP", "commit 9fceb02d0ae598e95dc970b74767f19372d61af8 by dev"),
		K("fp05", "FP", "request id 550e8400-e29b-41d4-a716-446655440000 finished"),
		K("fp06", "FP", "see /home/user/.config/app/secrets/readme.txt and internal/secretref/secretref.go"),
		K("fp07", "FP", "upgrade from v1.22.3-rc.1+build.5 to 2.0.0"),
		K("fp08", "FP", "processed 1234567890 rows in 12.5s at 2026-10-16T12:00:00Z"),
		K("fp09", "FP", "docker run -u 1000:1000 alpine id"),
		K("fp10", "FP", "llm --max-tokens 1000 --model x"),
		K("fp11", "FP", "OPENAI_API_KEY=op://Private/openai/key"),
		K("fp12", "FP", "token source: keychain://openai/api and op://vault/item/field"),
		K("fp13", "FP", `{"max_tokens": 1000, "usage": {"input_tokens": 12, "total_tokens": 20}}`),
		K("fp14", "FP", "usage prompt_tokens: 812 completion_tokens=40"),
		K("fp15", "FP", "Use Bearer auth for this API"),
		K("fp16", "FP", "Missing Authorization header; the password must be at least 12 characters"),
		K("fp17", "FP", "risk-assessment-documentation-v2 and task-runner-configuration-overview and disk-usage-monitoring-report"),
		K("fp18", "FP", "https://example.com/docs/auth?page=2&sort=asc"),
		K("fp19", "FP", "https://user@example.com/path"),
		K("fp20", "FP", "sha256 3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a done"),
		K("fp21", "FP", "kubectl get secret my-app-secret -n prod"),
		K("fp22", "FP", "tool --password-file /run/secrets/db_password --verbose"),
		K("fp23", "FP", "credential_missing: reload the plugin"),
		K("fp24", "FP", `plugin "onepassword": does not declare that operation`),
		K("fp25", "FP", `{"missing_secrets":["OPENAI_API_KEY","DB_PASSWORD"]}`),
		K("fp26", "FP", "AzureCLICredential: ERROR: AADSTS50076: multi-factor authentication required"),
		K("fp27", "FP", "Authorization: Bearer <token>"),
		K("fp28", "FP", "cache key: user:42:profile; token bucket refill every 5s"),
		K("fp29", "FP", "Set the API key with the credentials command, then retry."),
		K("fp30", "FP", "docker login -u bob --password-stdin registry.example.com"),
		K("fp31", "FP", "PASSWORD_MIN_LENGTH=12 and SECRET_ROTATION_DAYS=90"),
		K("fp32", "FP", "mapping of pin to footprint; password policy v3"),
	}
}

// options a case needs beyond the defaults, and cases that are documented gaps.
var (
	needsHeuristics = map[string]bool{"aw01": true}
	knownGaps       = map[string]string{
		"en04": "a bare base64 blob with no label cannot be told from data; only a known value (Scope) protects it",
	}
	needsBenignOnepassword = map[string]bool{"fp24": true}
)

func redactorFor(t *testing.T, id string) *redact.Redactor {
	t.Helper()
	var opts []redact.Option
	if needsHeuristics[id] {
		opts = append(opts, redact.WithHeuristics())
	}
	if needsBenignOnepassword[id] {
		opts = append(opts, redact.WithBenignKeys("onepassword"))
	}
	r, err := redact.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCorpus(t *testing.T) {
	var redactCases, keepCases int
	for _, c := range All() {
		t.Run(c.ID, func(t *testing.T) {
			if why, gap := knownGaps[c.ID]; gap {
				t.Skipf("known gap: %s", why)
			}
			r := redactorFor(t, c.ID)
			out := r.Text(c.Input)
			if again := r.Text(out); again != out {
				t.Errorf("not idempotent:\n  once : %q\n  twice: %q", out, again)
			}
			switch c.Expect {
			case "redact":
				redactCases++
				for _, s := range c.Secret {
					if strings.Contains(out, s) {
						t.Errorf("secret %q survived (%s)\n  in : %q\n  out: %q", s, c.Class, c.Input, out)
					}
				}
				for _, k := range c.Keep {
					if !strings.Contains(out, k) {
						t.Errorf("collateral damage: %q was lost (%s)\n  in : %q\n  out: %q", k, c.Class, c.Input, out)
					}
				}
			case "keep":
				keepCases++
				if out != c.Input {
					t.Errorf("false positive (%s)\n  in : %q\n  out: %q", c.Class, c.Input, out)
				}
			}
		})
	}
	if redactCases < 60 || keepCases < 30 {
		t.Errorf("the corpus shrank: %d redact cases, %d keep cases", redactCases, keepCases)
	}
}

// PII corpus is separate: the original credential corpus remains byte-for-byte
// compatible. Every PII-shaped test value is assembled at runtime.
func piiCorpus() []Case {
	var cases []Case
	for id, value := range map[string]string{
		"email":                 j("alice", "@", "example.test"),
		"unicode-email":         j("δοκιμή", "@", "παράδειγμα.δοκιμή"),
		"unicode-mark":          j("e\u0301lodie", "@", "exemple.test"),
		"nfkc-email":            j("ⓐlice", "＠", "example.test"),
		"ipv6-zone-colon":       j("::", "%0", "::"),
		"phone-adjacent-plus":   j("D+", "000000000000 ", "000000000 ", "00"),
		"phone-adjacent-paren":  j("0000000000", "(", "00000000000"),
		"card-next-to-ssn":      j("4111 ", "1111 ", "1111 ", "1111 ", "219-", "09-", "9999"),
		"phone-adjacent-groups": j("000000 ", "00000 ", "0000000000"),
		"nfkc-key":              j("ⓔmail=", "opaque"),
		"format-email":          j("al\u200dice", "@", "example.test"),
		"phone":                 j("+44 ", "20 7946 ", "0958"),
		"national-phone":        j("(212) ", "555-", "0199"),
		"ipv4":                  j("192.0.", "2.23"),
		"ipv6":                  j("2001:db8:", ":42"),
		"card":                  j("4111 ", "1111 ", "1111 ", "1111"),
		"nanite":                j("abcdef0123456789", "abcdef0123456789"),
		"iban":                  j("GB82", "WEST", "123456", "98765432"),
		"iban-spaces":           j("DE89 ", "3704 0044 ", "0532 0130 00"),
		"ssn":                   j("219-", "09-", "9999"),
		"nino":                  j("AB", "123456", "C"),
		"nino-spaces":           j("ab ", "12 34 ", "56 c"),
	} {
		secret := value
		if id == "phone-adjacent-plus" {
			secret = strings.TrimPrefix(value, "D")
		}
		if id == "nfkc-key" {
			secret = "opaque"
		}
		cases = append(cases, R(id, "PII", "before <"+value+"> after", []string{secret}, "before <", "> after"))
	}
	for id, value := range map[string]string{
		"overlong-card":     j("4111", "1111", "1111", "1111", "1234"),
		"bad-card":          j("4111 ", "1111 ", "1111 ", "1112"),
		"bad-ipv4":          j("999.0.", "2.23"),
		"bad-ipv6":          j("2001:db8:", ":xyz"),
		"bad-iban-checksum": j("GB83", "WEST", "123456", "98765432"),
		"bad-ssn-area-zero": j("000-", "09-", "9999"),
		"bad-ssn-area-666":  j("666-", "09-", "9999"),
		"bad-ssn-area-900":  j("900-", "09-", "9999"),
		"bad-ssn-group":     j("219-", "00-", "9999"),
		"bad-ssn-serial":    j("219-", "09-", "0000"),
		"bad-nino-prefix":   j("BG", "123456", "A"),
		"bad-nino-letter":   j("AQ", "123456", "A"),
		"bad-nino-second-o": j("AO", "123456", "A"),
		"phone-timestamp":   j("2026.10.", "16.120000"),
		"bad-nino-suffix":   j("AB", "123456", "E"),
		"order":             j("order=", "123456789"),
		"hash":              j("commit ", "abcdef0123456789", "abcdef0123456789", "abcdef01"),
		"uuid":              j("550e8400-", "e29b-41d4-", "a716-446655440000"),
		"version":           "v1.22.3-rc.1+build.5",
		"timestamp":         j("2026-10-16", "T12:34:56Z"),
		"date":              j("2026-", "10-16"),
		"metadata":          "email_count=42 phone_type=mobile first_name_count=3 birth_date_format=iso",
		"unlabeled-prose":   "Jane Example lives on Example Street",
		"bad-email-local":   j("a..b", "@", "example.test"),
		"bad-email-domain":  j("a", "@", "-example.test"),
	} {
		cases = append(cases, K(id, "PII-negative", value))
	}
	for _, key := range []string{"email", "phone", "ssn", "address", "dob", "birth_date", "first_name", "last_name", "full_name", "email_value", "user_phone_data"} {
		cases = append(cases, R("key-"+key, "PII-key", key+`="synthetic value"`, []string{"synthetic value"}, key))
	}
	return cases
}

func TestPIICorpus(t *testing.T) {
	r := mustNew(t, redact.WithPII())
	for _, c := range piiCorpus() {
		t.Run(c.ID, func(t *testing.T) {
			out := r.Text(c.Input)
			if (strings.HasPrefix(c.ID, "phone-adjacent") || c.ID == "card-next-to-ssn") && strings.ContainsAny(out, "0123456789") {
				t.Fatalf("adjacent PII fragment survived: %q", out)
			}
			if c.Expect == "keep" && out != c.Input {
				t.Fatalf("false positive: %q -> %q", c.Input, out)
			}
			for _, value := range c.Secret {
				if strings.Contains(out, value) {
					t.Errorf("value survived: %q", out)
				}
			}
			for _, keep := range c.Keep {
				if !strings.Contains(out, keep) {
					t.Errorf("collateral damage: %q", out)
				}
			}
			if again := r.Text(out); again != out {
				t.Errorf("not idempotent: %q -> %q", out, again)
			}
			if c.Expect == "redact" && redact.Default().Text(c.Input) != c.Input {
				t.Errorf("credential default changed for %s", c.ID)
			}
		})
	}
}
