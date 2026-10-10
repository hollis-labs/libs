//go:build !linux && !darwin

package snapshot

import (
	"context"
	"os"
)

type admissionLock struct{}

func openAdmission(string, CapturePolicy) (*Admission, error) { return nil, ErrAdmissionUnavailable }
func (a *Admission) lock(context.Context) (*admissionLock, error) {
	return nil, ErrAdmissionUnavailable
}
func (a *Admission) checkFile(*os.File, string) error { return ErrAdmissionUnavailable }
func (a *Admission) syncDirectory() error             { return ErrAdmissionUnavailable }
func (h *admissionLock) check() error                 { return ErrAdmissionUnavailable }
func (h *admissionLock) close() error                 { return nil }

func (a *Admission) openLedger() (*os.File, error) { return nil, ErrAdmissionUnavailable }

func (a *Admission) checkRoot() error { return ErrAdmissionUnavailable }

func admissionStoreIdentity(os.FileInfo) string { return "" }
