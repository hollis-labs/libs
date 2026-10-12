package forums

import (
	"errors"
	"strings"
	"testing"
)

func TestNamespaceAndOwnerPolicy(t *testing.T) {
	for _, ns := range []string{"app/torque/forum/portfolio", "app/test/forum/my_forum-1"} {
		id := strings.Split(ns, "/")[3]
		if e := ValidateNamespace(ns, id); e != nil {
			t.Fatal(e)
		}
	}
	for _, ns := range []string{"user/x/forum/x", "app/torque/forum/../escape", "app/torque/forum/other", "app//forum/portfolio", "app/torque/forum/topics"} {
		if e := ValidateNamespace(ns, "portfolio"); !errors.Is(e, ErrInvalid) {
			t.Fatalf("accepted invalid namespace %q", ns)
		}
	}
	forum := Forum{OwnerID: "owner", OwnerType: "agent"}
	if !CanAccess(forum, Caller{Provenance: Provenance{Principal: Principal{ID: "owner", Kind: "agent"}}}) {
		t.Fatal("owner refused")
	}
	if CanAccess(forum, Caller{Provenance: Provenance{Principal: Principal{ID: "owner", Kind: "user"}}}) || CanAccess(forum, Caller{Operator: true}) {
		t.Fatal("kind or empty principal bypass")
	}
}
func TestDateNormalizationAndBudget(t *testing.T) {
	opts := Options{CreatedAfter: "2026-10-12T12:00:00-04:00", CreatedBefore: "2026-10-12T17:00:00Z"}
	if e := validateOptions(&opts); e != nil {
		t.Fatal(e)
	}
	if opts.CreatedAfter != "2026-10-12T16:00:00.000000000Z" {
		t.Fatal("offset not normalized")
	}
	for _, o := range []Options{{Limit: 101}, {Limit: -1}, {SinceSeq: -1}, {MaxChars: 100001}, {Detail: "everything"}, {AuthorKind: "service"}, {CreatedAfter: "yesterday"}, {CreatedAfter: "2026-10-13T00:00:00Z", CreatedBefore: "2026-10-12T00:00:00Z"}} {
		if e := validateOptions(&o); !errors.Is(e, ErrInvalid) {
			t.Fatalf("accepted invalid options: %+v", o)
		}
	}
	pg := Page[Post]{Items: []Post{{BodyMD: strings.Repeat("🙂", 300), BodyChars: 300}, {BodyMD: "next", BodyChars: 4}}}
	compactPosts(&pg, Options{MaxChars: 3}, false)
	if pg.Items[0].Snippet != "🙂🙂🙂" || pg.Items[1].Snippet != "" || !pg.Items[0].Truncated || pg.Items[0].BodyMD != "" {
		t.Fatal("Unicode budget")
	}
}
