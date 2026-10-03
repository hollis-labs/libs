package federation

import (
	"errors"
	"reflect"
	"testing"
)

func TestDefaultOpSetIsTorquesSurface(t *testing.T) {
	d := DefaultOpSet()
	want := []Op{OpSend, OpGet, OpThread, OpConsume, OpCancel}
	if !reflect.DeepEqual(d.Ops(), want) {
		t.Fatalf("DefaultOpSet = %v, want %v", d.Ops(), want)
	}
	if d.Allows(OpInbox) || d.Allows(OpSubscribe) {
		t.Fatal("Inbox and Subscribe must be off by default (ratified 2026-09-30)")
	}
	d[OpInbox] = true // each call returns a fresh map
	if DefaultOpSet().Allows(OpInbox) {
		t.Fatal("DefaultOpSet must return a fresh map")
	}
}

func TestOpSetConstructionAndWidening(t *testing.T) {
	s, err := NewOpSet(OpSend, OpInbox)
	if err != nil || !s.Allows(OpSend) || !s.Allows(OpInbox) || s.Allows(OpGet) {
		t.Fatalf("NewOpSet = %v, %v", s, err)
	}
	if _, serr := NewOpSet("sned"); serr == nil {
		t.Error("an unknown operation must be an error, not a silently absent one")
	}
	w, err := DefaultOpSet().Widen(OpInbox, OpSubscribe)
	if err != nil || !w.Allows(OpInbox) || !w.Allows(OpSubscribe) || !w.Allows(OpSend) {
		t.Fatalf("Widen = %v, %v", w, err)
	}
	if _, err := DefaultOpSet().Widen("nope"); err == nil {
		t.Error("Widen must reject an unknown operation")
	}
	var nilSet OpSet
	if nilSet.Allows(OpSend) || len(nilSet.Ops()) != 0 {
		t.Error("a nil OpSet allows nothing")
	}
}

func TestOpSetValidateResolvesNilToTheDefaultAndRejectsNothing(t *testing.T) {
	var nilSet OpSet
	got, err := nilSet.validate()
	if err != nil || !reflect.DeepEqual(got.Ops(), DefaultOpSet().Ops()) {
		t.Fatalf("nil -> %v, %v", got, err)
	}
	if _, err := (OpSet{}).validate(); !errors.Is(err, ErrNoOps) {
		t.Errorf("an empty set: %v", err)
	}
	if _, err := (OpSet{OpSend: false}).validate(); !errors.Is(err, ErrNoOps) {
		t.Errorf("an all-false set: %v", err)
	}
	if _, err := (OpSet{"bogus": true}).validate(); err == nil {
		t.Error("an unknown key must be rejected")
	}
	if _, err := (OpSet{OpSend: true, "sned": true}).validate(); err == nil {
		t.Error("a typo'd operation beside a valid one must be rejected, not ignored")
	}
	in := OpSet{OpSend: true}
	out, _ := in.validate()
	out[OpGet] = true
	if in.Allows(OpGet) {
		t.Error("validate must return a private copy")
	}
}
