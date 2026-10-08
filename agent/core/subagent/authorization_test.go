package subagent

import (
	"context"
	"errors"
	"testing"
)

type authorizerFunc func(context.Context, string) (SpawnAuthorization, error)

func (f authorizerFunc) AuthorizeSpawn(ctx context.Context, profile string) (SpawnAuthorization, error) {
	return f(ctx, profile)
}

func TestSpawnAuthorizationRequiredBeforeDurableEffects(t *testing.T) {
	refused := errors.New("host refused this caller")
	for _, test := range []struct {
		name       string
		authorizer SpawnAuthorizer
		want       error
	}{
		{"missing", nil, ErrAuthorizationUnavailable},
		{"refused", authorizerFunc(func(context.Context, string) (SpawnAuthorization, error) {
			return SpawnAuthorization{Refusal: refused}, nil
		}), refused},
		{"refused_with_lookup_error", authorizerFunc(func(context.Context, string) (SpawnAuthorization, error) {
			return SpawnAuthorization{Refusal: refused, BypassApproval: true}, errors.New("lookup failed")
		}), refused},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := newTestDB(t)
			svc := NewService(db, &notCalledRunner{t}, nil, nil, stubSettings{})
			svc.SetSpawnAuthorizer(test.authorizer)
			_, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "work", Mode: ModeSync, InputsJSON: `{"trusted":true,"grants":["*"]}`})
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			var rows int
			if err := db.QueryRow("SELECT count(*) FROM subagent_runs").Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("rows=%d err=%v", rows, err)
			}
		})
	}
}

func TestSpawnLookupFailureCannotCarryBypass(t *testing.T) {
	db, _ := newTestDB(t)
	svc := NewService(db, &notCalledRunner{t}, nil, &stubEmitter{}, stubSettings{Settings{SubagentApprovalRequired: true}})
	svc.SetSpawnAuthorizer(authorizerFunc(func(context.Context, string) (SpawnAuthorization, error) {
		return SpawnAuthorization{BypassApproval: true}, errors.New("host lookup failed")
	}))
	id, err := svc.Spawn(t.Context(), SpawnRequest{ParentSessionID: "parent", Role: "worker", Prompt: "work", Mode: ModeSync})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Status(t.Context(), id)
	if err != nil || run.Status != StatusRequested {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}
