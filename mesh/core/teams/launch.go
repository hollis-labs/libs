package teams

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

type LaunchState string

const (
	Planning     LaunchState = "planning"
	Prepared     LaunchState = "prepared"
	Launched     LaunchState = "launched"
	MembersReady LaunchState = "members_ready"
	RoutingReady LaunchState = "routing_ready"
	Aborting     LaunchState = "aborting"
	Failed       LaunchState = "failed"
)

type LaunchRequest struct {
	Key     string
	TeamID  string
	Version uint64
	Counts  map[string]int
	Limits  mesh.Limits
}
type MemberIntent struct {
	Key      string
	Slot     Slot
	MemberID string
	Request  ProvisionRequest
	Cleaned  bool
	Member   *Member
}
type LaunchRecord struct {
	Attempts   int
	Deadline   time.Time
	Failure    string
	Key        string
	Digest     string
	State      LaunchState
	Team       Team // Immutable snapshot: later edits don't change recovery.
	Definition WorkflowDefinition
	Intents    []MemberIntent
	Limits     mesh.Limits
	Run        TeamRun
}
type Launcher struct {
	Definitions DefinitionStore
	Roster      RosterStore
	Ledger      LaunchLedger
	Provisioner MemberProvisioner
	Workflows   WorkflowLauncher
	Routing     RoutingInstaller
	Clock       Clock
	IDs         IDs
	MaxAttempts int // zero uses three attempts, bounded by the launch timeout
	Defaults    mesh.Limits
}

func stableID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("team-%x", digest[:16])
}
func requestDigest(req LaunchRequest) (string, error) {
	req.Key = ""
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
func (l *Launcher) configured() error {
	if l.Definitions == nil || l.Roster == nil || l.Ledger == nil || l.Provisioner == nil || l.Workflows == nil || l.Routing == nil || l.Clock == nil || l.IDs == nil {
		return fmt.Errorf("launch: incomplete host")
	}
	return nil
}
func (l *Launcher) Launch(ctx context.Context, req LaunchRequest) (TeamRun, error) {
	if err := l.configured(); err != nil {
		return TeamRun{}, err
	}
	if req.Key == "" {
		req.Key = l.IDs.NewID()
	}
	if req.Key == "" || req.TeamID == "" || req.Version == 0 {
		return TeamRun{}, fmt.Errorf("launch: key/team/version required")
	}
	digest, err := requestDigest(req)
	if err != nil {
		return TeamRun{}, err
	}
	var result TeamRun
	err = l.Ledger.WithLease(ctx, req.Key, func(ctx context.Context) error {
		record, err := l.Ledger.GetLaunch(ctx, req.Key)
		if errors.Is(err, ErrNotFound) {
			t, err := l.Definitions.GetDefinition(ctx, req.TeamID, req.Version)
			if err != nil {
				return err
			}
			definition, err := CompileTeam(t, t.Phases)
			if err != nil {
				return err
			}
			limits, err := ResolveLimits(req.Limits, t.Policy.Spawn, l.Defaults)
			if err != nil {
				return err
			}
			record = LaunchRecord{Key: req.Key, Digest: digest, State: Planning, Deadline: l.Clock.Now().Add(limits.Timeout), Team: clone(t), Definition: definition, Limits: limits}
			for name := range req.Counts {
				if _, ok := t.slot(name); !ok {
					return fmt.Errorf("launch: unknown slot override %s", name)
				}
			}
			for _, slot := range t.Slots {
				count := slot.Min
				if n, ok := req.Counts[slot.Name]; ok {
					count = n
				}
				if count < slot.Min || count > slot.Max {
					return fmt.Errorf("launch: slot %s outside min/max", slot.Name)
				}
				for i := 0; i < count; i++ {
					id := stableID(req.Key, slot.Name, fmt.Sprint(i))
					identity := slot.Identity
					if slot.Resolution == Pool {
						identity = slot.Identities[i]
					}
					allocation := limits
					record.Intents = append(record.Intents, MemberIntent{Key: id, Slot: clone(slot), MemberID: id, Request: ProvisionRequest{IdempotencyKey: id, MemberID: id, Slot: clone(slot), Identity: identity, ReservedIdentities: declaredIdentities(t), Limits: allocation}})
				}
			}
			if len(record.Intents) == 0 {
				return fmt.Errorf("launch: no eager members")
			}
			if len(record.Intents) > limits.FanOut || len(record.Intents) > limits.MaxChildren {
				return fmt.Errorf("launch: initial roster exceeds limits")
			}
			for i := range record.Intents {
				record.Intents[i].Request.Limits.Budget /= float64(len(record.Intents))
			}
			if err = l.Ledger.PutLaunch(ctx, record); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if record.Digest != digest {
			return ErrConflict
		}
		result, err = l.resume(ctx, record)
		return err
	})
	return result, err
}
func (l *Launcher) resume(ctx context.Context, record LaunchRecord) (TeamRun, error) {
	if record.State == RoutingReady {
		return record.Run, nil
	}
	if record.State == Failed {
		return record.Run, fmt.Errorf("%w: %s", ErrLaunchFailed, record.Failure)
	}
	if record.State == Aborting {
		return l.finishAbort(ctx, record)
	}
	switch record.State {
	case Planning, Prepared, Launched, MembersReady:
	default:
		return record.Run, fmt.Errorf("launch: invalid ledger state")
	}
	maxAttempts := l.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if record.Attempts >= maxAttempts || !l.Clock.Now().Before(record.Deadline) {
		return l.beginAbort(ctx, record, fmt.Errorf("launch retry/deadline budget exhausted"))
	}
	record.Attempts++
	if err := l.Ledger.PutLaunch(ctx, record); err != nil {
		return record.Run, err
	}
	run, err := l.advance(ctx, &record)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return run, err
	}
	if err != nil && (errors.Is(err, ErrConflict) || errors.Is(err, ErrProvisionFailed) || record.Attempts >= maxAttempts || !l.Clock.Now().Before(record.Deadline)) {
		return l.beginAbort(context.WithoutCancel(ctx), record, err)
	}
	return run, err
}
func (l *Launcher) advance(ctx context.Context, record *LaunchRecord) (TeamRun, error) {
	if record.State == Planning {
		for i := range record.Intents {
			intent := &record.Intents[i]
			if intent.Member != nil {
				continue
			}
			limits := intent.Request.Limits
			m, err := l.Provisioner.Provision(ctx, clone(intent.Request))
			if err != nil {
				return record.Run, err
			}
			m.ID = intent.MemberID
			m.Slot = intent.Slot.Name
			m.Governance = MemberRole
			m.Resolution = intent.Slot.Resolution
			m.JoinedAt = l.Clock.Now()
			m.Budget = limits.Budget
			m.Limits = limits
			m.Intent = clone(&intent.Request)
			if err = validateProvisioned(m, intent.Request); err != nil {
				return record.Run, errors.Join(ErrProvisionFailed, err)
			}
			for _, other := range record.Intents {
				if other.Member != nil && other.Member.Actor == m.Actor {
					return record.Run, ErrConflict
				}
			}
			intent.Member = &m
			if err = l.Ledger.PutLaunch(ctx, *record); err != nil {
				return record.Run, err
			}
		}
		record.State = Prepared
		if err := l.Ledger.PutLaunch(ctx, *record); err != nil {
			return record.Run, err
		}
	}
	if record.State == Prepared {
		runID, err := l.Workflows.LaunchWorkflow(ctx, record.Key, clone(record.Definition))
		if err != nil {
			return record.Run, err
		}
		if runID == "" {
			return record.Run, fmt.Errorf("launch: empty workflow run id")
		}
		record.Run = TeamRun{ID: runID, TeamID: record.Team.ID, TeamVersion: record.Team.Version, Channel: "team/" + runID, Status: mesh.TaskWorking}
		record.State = Launched
		if err = l.Ledger.PutLaunch(ctx, *record); err != nil {
			return record.Run, err
		}
	}
	if record.State == Launched {
		err := l.Roster.Mutate(ctx, record.Run.ID, func(r *Roster) error {
			for _, intent := range record.Intents {
				if intent.Member == nil {
					return fmt.Errorf("launch: missing prepared member")
				}
				found := false
				for _, m := range r.Members {
					if m.ID == intent.MemberID {
						if m.Actor != intent.Member.Actor || m.Slot != intent.Slot.Name {
							return ErrConflict
						}
						found = true
					}
					if m.Actor == intent.Member.Actor && m.ID != intent.MemberID && (intent.Slot.Resolution == Fresh || m.Status == "active") {
						return ErrConflict
					}
				}
				if !found {
					r.Members = append(r.Members, clone(*intent.Member))
				}
			}
			return nil
		})
		if err != nil {
			return record.Run, err
		}
		record.State = MembersReady
		if err = l.Ledger.PutLaunch(ctx, *record); err != nil {
			return record.Run, err
		}
	}
	if record.State == MembersReady {
		if err := l.Routing.InstallRouting(ctx, record.Key, record.Run, clone(record.Team.Routing)); err != nil {
			return record.Run, err
		}
		record.State = RoutingReady
		if err := l.Ledger.PutLaunch(ctx, *record); err != nil {
			return record.Run, err
		}
	}
	return record.Run, nil
}

type ReconcileReport struct {
	Inspected, Recovered int
	Next                 string
	Failures             []error
}

// Reconcile uses a caller-owned cursor. The host schedules it; this library
// has no background scheduler. Failed keys cannot starve later records.
func (l *Launcher) Reconcile(ctx context.Context, after string, limit int) (ReconcileReport, error) {
	if err := l.configured(); err != nil {
		return ReconcileReport{}, err
	}
	if limit <= 0 {
		return ReconcileReport{}, fmt.Errorf("reconcile: positive limit required")
	}
	keys, err := l.Ledger.Pending(ctx, after, limit)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.Inspected++
		report.Next = key
		err := l.Ledger.WithLease(ctx, key, func(ctx context.Context) error {
			record, err := l.Ledger.GetLaunch(ctx, key)
			if err != nil {
				return err
			}
			_, err = l.resume(ctx, record)
			return err
		})
		if err != nil {
			report.Failures = append(report.Failures, fmt.Errorf("%s: %w", key, err))
		} else {
			report.Recovered++
		}
	}
	return report, nil
}

// Abort journals failure before cleanup and fences every planned intent,
// including resources whose provisioning acknowledgement was lost.
func (l *Launcher) beginAbort(ctx context.Context, record LaunchRecord, cause error) (TeamRun, error) {
	record.State = Aborting
	record.Failure = cause.Error()
	record.Run.Status = mesh.TaskFailed
	if err := l.Ledger.PutLaunch(ctx, record); err != nil {
		return record.Run, errors.Join(cause, err)
	}
	return l.finishAbort(ctx, record)
}
func (l *Launcher) finishAbort(ctx context.Context, record LaunchRecord) (TeamRun, error) {
	failure := fmt.Errorf("%w: %s", ErrLaunchFailed, record.Failure)
	// Fence workflow launch before cleanup so a delayed acknowledgement cannot
	// recreate an executing run. Failure status is durable during cleanup.
	if err := l.Workflows.FailWorkflow(ctx, record.Key, record.Failure); err != nil {
		return record.Run, errors.Join(failure, err)
	}
	var plan []Member
	if record.Run.ID != "" {
		if err := l.Roster.Mutate(ctx, record.Run.ID, func(r *Roster) error {
			for i, m := range r.Members {
				if liveMember(m) {
					r.Members[i].Status = terminationState(m)
				}
			}
			for _, root := range r.Members {
				if root.Parent != "" {
					continue
				}
				subtree, err := Cascade(*r, root.ID, true)
				if err != nil {
					return err
				}
				for _, m := range subtree {
					if terminating(m) {
						plan = append(plan, m)
					}
				}
			}
			return nil
		}); err != nil {
			return record.Run, errors.Join(failure, err)
		}
	}
	if err := finishMembers(ctx, l.Roster, l.Provisioner, record.Run.ID, plan); err != nil {
		return record.Run, errors.Join(failure, err)
	}
	for i := range record.Intents {
		intent := &record.Intents[i]
		if intent.Cleaned {
			continue
		}
		m := Member{ID: intent.MemberID, Slot: intent.Slot.Name, Resolution: intent.Slot.Resolution, Intent: clone(&intent.Request)}
		if intent.Member != nil {
			m = clone(*intent.Member)
			m.Intent = clone(&intent.Request)
		}
		if err := releaseOrStop(ctx, l.Provisioner, record.Key, m); err != nil {
			return record.Run, errors.Join(failure, err)
		}
		intent.Cleaned = true
		if err := l.Ledger.PutLaunch(ctx, record); err != nil {
			return record.Run, errors.Join(failure, err)
		}
	}
	if record.Run.ID != "" {
		if err := l.Routing.RemoveRouting(ctx, record.Run.ID); err != nil {
			return record.Run, errors.Join(failure, err)
		}
	}
	record.State = Failed
	record.Run.Status = mesh.TaskFailed
	if err := l.Ledger.PutLaunch(ctx, record); err != nil {
		return record.Run, errors.Join(failure, err)
	}
	return record.Run, failure
}
