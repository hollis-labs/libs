package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	secrets "github.com/hollis-labs/go-secrets"
	"github.com/hollis-labs/go-secrets/secretstest"
)

func TestSetFromStdinThenCheckNeverPrintsTheValue(t *testing.T) {
	const value = "s3cr3t-value-for-test-1234"
	r := secrets.New(secrets.WithStateDir(t.TempDir()), secrets.WithStore(&secretstest.FakeStore{}))
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"set", "file://app/key"}, strings.NewReader(value+"\n"), &out, &errb, r); code != 0 {
		t.Fatalf("set exit %d: %s", code, errb.String())
	}
	if code := run(context.Background(), []string{"check", "file://app/key"}, nil, &out, &errb, r); code != 0 {
		t.Fatalf("check exit %d: %s", code, errb.String())
	}
	secretstest.AssertNoLeak(t, value, out.String(), errb.String())
	if !strings.Contains(out.String(), "source=file") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	r := secrets.New(secrets.WithStateDir(t.TempDir()), secrets.WithStore(&secretstest.FakeStore{}))
	var out, errb bytes.Buffer
	for _, args := range [][]string{{}, {"set"}, {"set", "file://a/b", "extra-secret-arg"}, {"nope", "file://a/b"}} {
		if code := run(context.Background(), args, nil, &out, &errb, r); code != 2 {
			t.Errorf("args %v: exit %d, want 2", args, code)
		}
	}
}
