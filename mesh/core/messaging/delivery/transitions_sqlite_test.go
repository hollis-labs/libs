package delivery_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/messaging/delivery"
	"github.com/hollis-labs/libs/mesh/core/messaging/deliverytest"
)

func TestSQLiteTransitionFailureRollsBackMutation(t *testing.T) {
	for _, operation := range []string{"enqueue", "claim", "consume", "retry", "dead_letter", "redrive", "expiry", "claim_deadline", "ack_deadline"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			db := openSQLite(t, sqliteMemoryDSN(t))
			t.Cleanup(func() { _ = db.Close() })
			if err := delivery.ApplySQLiteSchema(ctx, db); err != nil {
				t.Fatal(err)
			}
			clock := deliverytest.NewFakeClock(time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC))
			store := delivery.NewSQLiteStore(db, delivery.WithSQLiteClock(clock))
			req := sqliteBasicRequest("atomic", agent("alice"), agent("bob"))
			req.DeadlineAt = clock.Now().Add(time.Hour)
			var d delivery.RecipientDelivery
			var a delivery.Attempt
			if operation != "enqueue" {
				d = mustEnqueue(t, store, req).Deliveries[0]
				if operation != "claim" && operation != "claim_deadline" {
					leaseDuration := time.Minute
					if operation == "ack_deadline" {
						leaseDuration = 2 * time.Hour
					}
					claim, err := store.Claim(ctx, sqliteClaimFor(d.ID, d.Recipient, "host", leaseDuration, 1))
					if err != nil {
						t.Fatal(err)
					}
					a = claim.Attempt
				}
				if operation == "redrive" {
					if _, _, err := store.Nack(ctx, delivery.NackRequest{Lease: sqliteLeaseRef(a)}); err != delivery.ErrDeadLettered {
						t.Fatal(err)
					}
				}
			}
			mutate := func() error {
				var err error
				switch operation {
				case "enqueue":
					_, err = store.Enqueue(ctx, req)
				case "claim", "expiry", "claim_deadline":
					_, err = store.Claim(ctx, sqliteClaimFor(d.ID, d.Recipient, "host", time.Minute, 1))
				case "consume", "ack_deadline":
					_, _, err = store.Ack(ctx, delivery.AckRequest{Lease: sqliteLeaseRef(a), Stage: delivery.StageConsumed})
				case "retry", "dead_letter":
					_, _, err = store.Nack(ctx, delivery.NackRequest{Lease: sqliteLeaseRef(a), Retryable: operation == "retry", Error: "failed"})
				case "redrive":
					_, err = store.Redrive(ctx, delivery.RedriveRequest{DeliveryID: d.ID, AuthorizedBy: "operator"})
				}
				return err
			}
			before := deliveryDBSnapshot(t, db)
			if operation == "expiry" {
				clock.Advance(2 * time.Minute)
			}
			if operation == "claim_deadline" || operation == "ack_deadline" {
				clock.Advance(time.Hour)
			}
			if _, err := db.Exec(`CREATE TRIGGER reject_transition BEFORE INSERT ON messaging_transitions BEGIN SELECT RAISE(ABORT, 'transition rejected'); END`); err != nil {
				t.Fatal(err)
			}
			if err := mutate(); err == nil || !strings.Contains(err.Error(), "transition rejected") {
				t.Fatalf("expected transition failure, got %v", err)
			}
			if after := deliveryDBSnapshot(t, db); !reflect.DeepEqual(before, after) {
				t.Fatalf("partial mutation after log failure: before=%v after=%v", before, after)
			}
			if _, err := db.Exec(`DROP TRIGGER reject_transition`); err != nil {
				t.Fatal(err)
			}
			if err := mutate(); err != nil && err != delivery.ErrDeadLettered && err != delivery.ErrDeadlineExceeded {
				t.Fatal(err)
			}
			if after := deliveryDBSnapshot(t, db); reflect.DeepEqual(before, after) {
				t.Fatal("mutation did not recover after log write restored")
			}
		})
	}
}

// Read raw tables: Store.GetDelivery may itself release an expired lease.
func deliveryDBSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, table := range []string{"messaging_messages", "messaging_deliveries", "messaging_attempts", "messaging_receipts", "messaging_idempotency", "messaging_transitions"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if b, ok := value.([]byte); ok {
					values[i] = string(b)
				}
			}
			out[table] = append(out[table], values)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestSQLiteTransitionSchemaUpgradeDoesNotInventHistory(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t, sqliteMemoryDSN(t))
	t.Cleanup(func() { _ = db.Close() })
	if err := delivery.ApplySQLiteSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := delivery.NewSQLiteStore(db)
	d := mustEnqueue(t, store, sqliteBasicRequest("before-upgrade", agent("alice"), agent("bob"))).Deliveries[0]
	// Version 1 has the same delivery tables but no transition log.
	if _, err := db.Exec(`DROP TABLE messaging_transitions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE messaging_delivery_schema SET version=1`); err != nil {
		t.Fatal(err)
	}
	if err := delivery.ApplySQLiteSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Transitions(ctx, 0, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("invented pre-upgrade history: %v %v", rows, err)
	}
	claim, err := store.Claim(ctx, sqliteClaimFor(d.ID, d.Recipient, "host", time.Minute, 1))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Transitions(ctx, 0, 10)
	if err != nil || len(before) != 1 || before[0].From != delivery.DeliveryPending || before[0].To != delivery.DeliveryLeased || before[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("post-upgrade: %v %v", before, err)
	}
	if err := delivery.ApplySQLiteSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	after, err := store.Transitions(ctx, 0, 10)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("schema reapply changed log: %v %v", after, err)
	}
}
