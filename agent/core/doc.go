// Package agent contains embeddable native agent runtime mechanisms.
//
// Context assembly, provider turns, iteration control, tool scheduling, bound
// approvals and child lifecycle are exposed through subpackages. The service
// package supplies per-run canonical reduction, status, snapshot and replay;
// transport/httpstream writes those events after host authorization.
// Applications supply preparation, models, policy, storage, tools and admission.
package agent
