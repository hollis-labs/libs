//go:build !linux && !darwin

package materialize

import (
	"context"
	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

func preflightPublicationCustody(context.Context, PublishRequest) (*publicationCustody, error) {
	return nil, ErrUnsupportedOperation
}

type publicationNative struct{}

func openPublicationNative(PublishRequest) (*publicationNative, error) {
	return nil, ErrUnsupportedOperation
}
func (*publicationNative) close() error                { return ErrUnsupportedOperation }
func (*publicationNative) rename(string, string) error { return ErrUnsupportedOperation }
func (*publicationNative) validate(context.Context, publication.Journal, publication.Phase) error {
	return ErrUnsupportedOperation
}
