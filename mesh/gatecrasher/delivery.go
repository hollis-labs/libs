package gatecrasher

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
)

// Deliver reloads the authoritative decision, leases one exact route and reports
// delivery separately. After an ambiguous send or abandoned lease it reconciles
// before retrying. A transport adapter MUST fence the live origin through the
// actual write: a preflight Validate alone cannot prevent runtime replacement.
func (c *Coordinator) Deliver(ctx context.Context, t gate.Ticket) (gate.DeliveryReceipt, error) {
	r, err := c.route(ctx, t)
	if err != nil {
		return gate.DeliveryReceipt{}, err
	}
	// Historical observations remain readable after expiry or replacement.
	// Only a NEW native send below requires current authority and a live origin.
	now := c.cfg.Clock()
	if now.Before(r.LeaseUntil) {
		return gate.DeliveryReceipt{}, ErrBusy
	}
	if r.Delivery != nil && (r.Delivery.Status == gate.DeliveryAcknowledged || r.Delivery.Status == gate.DeliveryRejected) {
		return *r.Delivery, nil
	}
	v, err := c.getItem(ctx, r.Request, t)
	if err != nil {
		return gate.DeliveryReceipt{}, err
	}
	res, err := c.observe(r, v)
	if err != nil {
		return gate.DeliveryReceipt{}, err
	}
	if res.State == gate.Pending {
		return gate.DeliveryReceipt{}, ErrPending
	}
	ref := resolutionRef(res)
	if r.DecisionRef != "" && r.DecisionRef != ref {
		return gate.DeliveryReceipt{}, gate.ErrBinding
	}
	uncertain := r.Delivery != nil && (r.Delivery.Status == gate.DeliveryUnknown || r.Delivery.Status == gate.DeliverySent)
	r.DecisionRef = ref
	r.LeaseUntil = now.Add(c.cfg.LeaseDuration)
	r.Attempts++
	r.Delivery = &gate.DeliveryReceipt{DecisionRef: ref, Binding: r.Request.Binding, AdapterID: r.Request.Adapter.AdapterID,
		NativeRequestID: r.Request.NativeRequestID, Status: gate.DeliveryUnknown}
	r, err = c.cfg.Journal.Swap(ctx, r.Request.IdempotencyKey, r.Version, r)
	if err != nil {
		return gate.DeliveryReceipt{}, err
	}
	if uncertain {
		receipt, reconcileErr := c.cfg.Origin.Reconcile(ctx, r.Request, res)
		if reconcileErr != nil {
			return c.finish(ctx, r, *r.Delivery, ErrUnknown)
		}
		if err := checkReceipt(r, res, receipt); err != nil {
			return c.finish(ctx, r, *r.Delivery, err)
		}
		if receipt.Status != gate.DeliveryPending {
			return c.finish(ctx, r, receipt, reconcileState(receipt.Status))
		}
		if receipt.EvidenceRef == "" {
			return c.finish(ctx, r, *r.Delivery, ErrUnknown)
		}
	}
	// The lease is a scheduling fence, not a distributed effect lock. Origin
	// revalidation/idempotency remains mandatory even when a lease expires
	// while a slow previous sender is still running.
	if res.Decision != nil {
		if err := c.cfg.Verifier.Verify(ctx, r.Request, *res.Decision); err != nil {
			return c.finish(ctx, r, *r.Delivery, gate.ErrAuthority)
		}
		for _, o := range r.Request.Options {
			if o.ID == res.Decision.OptionID && o.Kind == gate.Allow {
				if err := gate.ValidateAdmission(ctx, r.Request, *res.Decision, gate.Admission{Binding: r.Request.Binding, Adapter: r.Request.Adapter, EnforcementPoint: r.Request.EnforcementPoint, OperationDigest: r.Request.OperationDigest, Now: c.cfg.Clock()}, c.cfg.Verifier); err != nil {
					return c.finish(ctx, r, *r.Delivery, err)
				}
			}
		}
	}
	if err := c.cfg.Origin.Validate(ctx, r.Request); err != nil {
		return c.finish(ctx, r, *r.Delivery, err)
	}
	receipt, sendErr := c.cfg.Origin.Send(ctx, r.Request, res)
	if sendErr != nil {
		return c.finish(ctx, r, *r.Delivery, ErrUnknown)
	}
	if err := checkReceipt(r, res, receipt); err != nil {
		return c.finish(ctx, r, *r.Delivery, err)
	}
	if receipt.Status == gate.DeliveryPending {
		return c.finish(ctx, r, *r.Delivery, ErrUnknown)
	}
	return c.finish(ctx, r, receipt, reconcileState(receipt.Status))
}

func reconcileState(s gate.DeliveryStatus) error {
	if s == gate.DeliveryUnknown || s == gate.DeliverySent {
		return ErrUnknown
	}
	return nil
}

func checkReceipt(r Route, res gate.Resolution, p gate.DeliveryReceipt) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.DecisionRef != resolutionRef(res) || p.Binding != r.Request.Binding || p.AdapterID != r.Request.Adapter.AdapterID || !p.NativeRequestID.Equal(r.Request.NativeRequestID) {
		return gate.ErrBinding
	}
	return nil
}

func (c *Coordinator) finish(ctx context.Context, r Route, p gate.DeliveryReceipt, result error) (gate.DeliveryReceipt, error) {
	r.Delivery = &p
	r.LeaseUntil = time.Time{}
	_, err := c.cfg.Journal.Swap(ctx, r.Request.IdempotencyKey, r.Version, r)
	if err != nil {
		return p, err
	}
	return p, result
}

// RecordDelivery accepts trusted host reports only for an established delivery
// correlation. It does not permit callers to authorize sends or overwrite the
// authoritative human decision. Public APIs must authenticate their reporters.
func (c *Coordinator) RecordDelivery(ctx context.Context, p gate.DeliveryReceipt) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Status == gate.DeliveryPending {
		return gate.ErrUnsupported
	}
	for {
		r, err := c.cfg.Journal.FindDecision(ctx, p.DecisionRef, p.Binding)
		if err != nil {
			return err
		}
		if c.cfg.Clock().Before(r.LeaseUntil) {
			return ErrBusy
		}
		v, err := c.getItem(ctx, r.Request, *r.Ticket)
		if err != nil {
			return err
		}
		res, err := c.observe(r, v)
		if err != nil {
			return err
		}
		if err := checkReceipt(r, res, p); err != nil {
			return err
		}
		if r.Delivery != nil && (r.Delivery.Status == gate.DeliveryAcknowledged || r.Delivery.Status == gate.DeliveryRejected) {
			if r.Delivery.Status != p.Status {
				return ErrConflict
			}
			return nil
		}
		r.Delivery = &p
		_, err = c.cfg.Journal.Swap(ctx, r.Request.IdempotencyKey, r.Version, r)
		if !errors.Is(err, ErrCAS) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// RecordExecution stores observed reports without executing, claiming a grant,
// or turning delivery success into execution success. Unknown may be followed
// by a host-reconciled report carrying EvidenceRef; reports are append-only.
func (c *Coordinator) RecordExecution(ctx context.Context, p gate.ExecutionReceipt) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for {
		r, err := c.cfg.Journal.FindDecision(ctx, p.DecisionRef, p.Binding)
		if err != nil {
			return err
		}
		if p.OperationID != r.Request.OperationID {
			return gate.ErrBinding
		}
		if len(r.Executions) > 0 {
			prev := r.Executions[len(r.Executions)-1]
			if prev == p {
				return nil
			}
			if prev.State == gate.ExecutionSucceeded || prev.State == gate.ExecutionFailed || prev.State == gate.ExecutionCancelled {
				return ErrConflict
			}
			if prev.State == gate.ExecutionUnknown && p.State != gate.ExecutionUnknown && p.EvidenceRef == "" {
				return ErrUnknown
			}
			if prev.State == gate.ExecutionStarted && p.State == gate.ExecutionNotStarted {
				return ErrConflict
			}
		}
		r.Executions = append(r.Executions, p)
		_, err = c.cfg.Journal.Swap(ctx, r.Request.IdempotencyKey, r.Version, r)
		if !errors.Is(err, ErrCAS) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
