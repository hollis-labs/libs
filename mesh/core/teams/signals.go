package teams

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/substrate/mesh"
)

type PhaseSignalRecord struct {
	RunID, PhaseID string
	Actor          mesh.URN
	RosterVersion  uint64
}
type SignalResolution struct {
	RunID, PhaseID string
	Actor          mesh.URN // The actor authorized to advance, separate from the signaler.
	Signaler       mesh.URN
	RosterVersion  uint64
	MemberIDs      []string
	Output         string
}

// Signal records a permitted member's true trigger. It never closes a phase.
func Signal(ctx context.Context, t Team, r Roster, p Phase, actor mesh.URN, store SignalStore, evaluator TriggerEvaluator) (PhaseSignalRecord, error) {
	if store == nil || evaluator == nil {
		return PhaseSignalRecord{}, fmt.Errorf("signal: incomplete host")
	}
	authored, err := t.phase(p.ID)
	if err != nil {
		return PhaseSignalRecord{}, err
	}
	p = authored
	from, err := r.member(actor)
	if err != nil {
		return PhaseSignalRecord{}, err
	}
	active := false
	for _, slot := range p.ActiveSlots {
		if slot == from.Slot {
			active = true
		}
	}
	if !active {
		return PhaseSignalRecord{}, ErrDenied
	}
	if err = Authorize(t, from, from, MaySignalPhase, ""); err != nil {
		return PhaseSignalRecord{}, err
	}
	fired, err := evaluator.Evaluate(ctx, p.ExitTrigger, from)
	if err != nil {
		return PhaseSignalRecord{}, err
	}
	if !fired {
		return PhaseSignalRecord{}, ErrUnavailable
	}
	record := PhaseSignalRecord{RunID: r.RunID, PhaseID: p.ID, Actor: actor, RosterVersion: r.Version}
	return record, store.RecordSignal(ctx, record)
}

// Advance lets the owner, explicitly authorized slot, or (when Auto) any
// permitted member close a phase after a valid signal. The durable first-wins
// resolution is handed to the host's workflow; no agent execution occurs here.
func Advance(ctx context.Context, t Team, r Roster, p Phase, actor mesh.URN, store SignalStore) (SignalResolution, error) {
	if store == nil {
		return SignalResolution{}, fmt.Errorf("advance: missing store")
	}
	authored, err := t.phase(p.ID)
	if err != nil {
		return SignalResolution{}, err
	}
	p = authored
	from, err := r.member(actor)
	if err != nil {
		return SignalResolution{}, err
	}
	if err = Authorize(t, from, from, MaySignalPhase, ""); err != nil {
		return SignalResolution{}, err
	}
	authorized := p.ExitTrigger.AuthorizedSlot
	if authorized == "" && !p.ExitTrigger.Auto {
		authorized = p.OwnerSlot
		if authorized == "" {
			authorized = t.Routing.CoordinatorSlot
		}
	}
	if authorized != "" && from.Slot != authorized {
		return SignalResolution{}, ErrDenied
	}
	if authorized == "" && !p.ExitTrigger.Auto {
		return SignalResolution{}, ErrDenied
	}
	if existing, err := store.GetSignal(ctx, r.RunID, p.ID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return SignalResolution{}, err
	}
	signals, err := store.ListSignals(ctx, r.RunID, p.ID)
	if err != nil {
		return SignalResolution{}, err
	}
	if len(signals) == 0 {
		return SignalResolution{}, ErrUnavailable
	}
	result := SignalResolution{RunID: r.RunID, PhaseID: p.ID, Actor: actor, Signaler: signals[0].Actor, RosterVersion: r.Version, Output: "exit trigger satisfied"}
	for _, m := range r.Members {
		if m.Status != "active" {
			continue
		}
		for _, slot := range p.ActiveSlots {
			if slot == m.Slot {
				result.MemberIDs = append(result.MemberIDs, m.ID)
				break
			}
		}
	}
	return store.ResolveSignal(ctx, result)
}
