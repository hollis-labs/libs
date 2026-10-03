package artifactfs

import (
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/workflow/values"
)

func TestNewRejectsEmptyAndInvalidAuthority(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"", " ", "Hadron", "1abc", "-abc", "a_b", "a/b", "a b", "hadron-local\n", strings.Repeat("a", 64)} {
		if _, err := New(canonicalTestRoot(t), authority, &recordingAuthorizer{}); !errors.Is(err, values.ErrArtifactAuthority) {
			t.Errorf("authority %q error=%v", authority, err)
		}
	}
	for _, authority := range []string{"a", "hadron-local", "nanite-content-addressed-artifacts", "a.b-c9", strings.Repeat("a", 63)} {
		if _, err := New(canonicalTestRoot(t), authority, &recordingAuthorizer{}); err != nil {
			t.Errorf("authority %q error=%v", authority, err)
		}
	}
}

func TestNewValidatesExternalAuthorities(t *testing.T) {
	t.Parallel()
	delegate := &fakeExternalStore{}
	cases := map[string][]Option{
		"local authority": {WithExternal("hadron-local", delegate)},
		"malformed":       {WithExternal("Vault", delegate)},
		"nil delegate":    {WithExternal("vault", nil)},
		"duplicate":       {WithExternal("vault", delegate), WithExternal("vault", delegate)},
	}
	for name, options := range cases {
		if _, err := New(canonicalTestRoot(t), "hadron-local", &recordingAuthorizer{}, options...); !errors.Is(err, values.ErrArtifactAuthority) {
			t.Errorf("%s error=%v", name, err)
		}
	}
	if _, err := New(canonicalTestRoot(t), "hadron-local", &recordingAuthorizer{}, nil, WithExternal("vault", delegate)); err != nil {
		t.Fatalf("valid external: %v", err)
	}
}

func TestForeignAuthorityWithoutDelegateIsAuthorityError(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, t.TempDir(), &recordingAuthorizer{}, nil)
	foreign := externalMetadata([]byte("external"), values.RedactionPrivate).Ref
	access := testAccess()
	if _, err := store.Stat(t.Context(), access, foreign); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Stat error=%v", err)
	}
	if _, err := store.Open(t.Context(), access, foreign); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Open error=%v", err)
	}
	if _, err := store.Delete(t.Context(), values.ArtifactDeleteRequest{Access: access, Ref: foreign}); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Delete error=%v", err)
	}
	if _, err := store.Cleanup(t.Context(), values.ArtifactCleanupRequest{Access: access, Kind: values.ArtifactCleanupExternal, Ref: &foreign}); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Cleanup error=%v", err)
	}
	put := testPutRequest(values.RetentionRun, values.RedactionPrivate, "run-1")
	put.Store = "vault"
	if _, err := store.Put(t.Context(), put, strings.NewReader("x")); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Put foreign store error=%v", err)
	}
	put.Store = "Not Canonical"
	if _, err := store.Put(t.Context(), put, strings.NewReader("x")); !errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Put malformed store error=%v", err)
	}
}

func TestMalformedLocalReferenceIsInvalidNotAuthority(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, t.TempDir(), &recordingAuthorizer{}, nil)
	ref := externalMetadata([]byte("x"), values.RedactionPrivate).Ref
	ref.Store = testAuthority
	ref.Retention = values.RetentionRun
	ref.URI = "artifact://" + testAuthority + "/run/short/short"
	if _, err := store.Stat(t.Context(), testAccess(), ref); !errors.Is(err, values.ErrArtifactInvalid) || errors.Is(err, values.ErrArtifactAuthority) {
		t.Fatalf("Stat error=%v", err)
	}
}

func TestDeleteOfExternalReferenceRequiresExternalRetention(t *testing.T) {
	t.Parallel()
	delegate := &fakeExternalStore{metadata: externalMetadata([]byte("x"), values.RedactionPrivate)}
	store := newTestStore(t, t.TempDir(), &recordingAuthorizer{}, map[string]values.ArtifactStore{"vault": delegate})
	ref := delegate.metadata.Ref
	ref.Retention = values.RetentionProject
	if _, err := store.Delete(t.Context(), values.ArtifactDeleteRequest{Access: testAccess(), Ref: ref}); !errors.Is(err, values.ErrArtifactRetention) {
		t.Fatalf("Delete error=%v", err)
	}
	if delegate.deleteCalls != 0 {
		t.Fatalf("delegate deletes=%d", delegate.deleteCalls)
	}
}

func TestOwnerClaimAuthorizerMatrix(t *testing.T) {
	t.Parallel()
	const authority = "hadron-local"
	access := func(principal, run, project string) values.ArtifactAccess {
		access := testAccess()
		access.Principal, access.RunID, access.ProjectID = principal, run, project
		return access
	}
	store := newAuthorityTestStore(t, t.TempDir(), authority, OwnerClaimAuthorizer(authority, ""), nil)
	runPut := testPutRequest(values.RetentionRun, values.RedactionPrivate, "run-a")
	runPut.Access = access("worker", "run-a", "")
	runMeta, err := store.Put(t.Context(), runPut, strings.NewReader("run"))
	if err != nil {
		t.Fatalf("Put with exact run claim: %v", err)
	}
	projectPut := testPutRequest(values.RetentionProject, values.RedactionPrivate, "project-a")
	projectPut.Access = access("worker", "", "project-a")
	projectMeta, err := store.Put(t.Context(), projectPut, strings.NewReader("project"))
	if err != nil {
		t.Fatalf("Put with exact project claim: %v", err)
	}

	unauthorized := values.ErrArtifactUnauthorized
	denyPut := runPut
	denyPut.Access = access("worker", "run-b", "project-a")
	if _, err := store.Put(t.Context(), denyPut, strings.NewReader("run")); !errors.Is(err, unauthorized) {
		t.Errorf("Put with wrong run claim error=%v", err)
	}

	type step struct {
		name    string
		access  values.ArtifactAccess
		ref     values.ArtifactRef
		allowed bool
	}
	steps := []step{
		// Owner==nil pre-resolution path: the claim must hash to the owner segment.
		{"run ref, exact run claim", access("worker", "run-a", ""), runMeta.Ref, true},
		{"run ref, wrong run claim", access("worker", "run-b", ""), runMeta.Ref, false},
		{"run ref, project claim only", access("worker", "", "run-a"), runMeta.Ref, false},
		{"run ref, no claim", access("worker", "", ""), runMeta.Ref, false},
		{"project ref, exact project claim", access("worker", "", "project-a"), projectMeta.Ref, true},
		{"project ref, run claim only", access("worker", "project-a", ""), projectMeta.Ref, false},
		{"project ref, wrong project claim", access("worker", "", "project-b"), projectMeta.Ref, false},
		{"maintenance principal, no claim", access("system", "", ""), runMeta.Ref, true},
	}
	for _, step := range steps {
		_, err := store.Stat(t.Context(), step.access, step.ref)
		if step.allowed && err != nil {
			t.Errorf("%s: Stat error=%v", step.name, err)
		}
		if !step.allowed && !errors.Is(err, unauthorized) {
			t.Errorf("%s: Stat error=%v, want unauthorized", step.name, err)
		}
		_, err = store.Open(t.Context(), step.access, step.ref)
		if step.allowed != (err == nil) {
			t.Errorf("%s: Open error=%v", step.name, err)
		}
	}

	// Cleanup without the maintenance principal: owner claim is not enough for
	// the global kinds, and a ref-less request is unauthorized.
	worker := access("worker", "run-a", "project-a")
	for _, request := range []values.ArtifactCleanupRequest{
		{Access: worker, Kind: values.ArtifactCleanupNone},
		{Access: worker, Kind: values.ArtifactCleanupExpired, Before: worker.At},
		{Access: worker, Kind: values.ArtifactCleanupPartials, Before: worker.At},
	} {
		if _, err := store.Cleanup(t.Context(), request); !errors.Is(err, unauthorized) {
			t.Errorf("cleanup %s without maintenance error=%v", request.Kind, err)
		}
	}
	// A run-scoped cleanup needs the exact claim for the owner being cleaned.
	wrong := values.ArtifactCleanupRequest{Access: access("worker", "run-b", ""), Kind: values.ArtifactCleanupRun, Owner: runPut.Owner}
	if _, err := store.Cleanup(t.Context(), wrong); !errors.Is(err, unauthorized) {
		t.Errorf("cleanup with wrong claim error=%v", err)
	}
	// Deleting with a wrong claim never removes the artifact.
	if _, err := store.Delete(t.Context(), values.ArtifactDeleteRequest{Access: access("worker", "run-b", ""), Ref: runMeta.Ref}); !errors.Is(err, unauthorized) {
		t.Errorf("Delete with wrong claim error=%v", err)
	}
	// Maintenance may run every cleanup kind.
	maintenance := access("system", "", "")
	for _, kind := range []values.ArtifactCleanupKind{values.ArtifactCleanupNone, values.ArtifactCleanupExpired, values.ArtifactCleanupPartials} {
		request := values.ArtifactCleanupRequest{Access: maintenance, Kind: kind}
		if kind != values.ArtifactCleanupNone {
			request.Before = maintenance.At
		}
		if _, err := store.Cleanup(t.Context(), request); err != nil {
			t.Errorf("maintenance cleanup %s error=%v", kind, err)
		}
	}
	if _, err := store.Cleanup(t.Context(), values.ArtifactCleanupRequest{Access: worker, Kind: values.ArtifactCleanupRun, Owner: runPut.Owner}); err != nil {
		t.Errorf("owner run cleanup error=%v", err)
	}
	if _, err := store.Delete(t.Context(), values.ArtifactDeleteRequest{Access: access("worker", "", "project-a"), Ref: projectMeta.Ref}); err != nil {
		t.Errorf("owner Delete error=%v", err)
	}
}

func TestOwnerClaimAuthorizerCustomMaintenancePrincipalAndForeignRefs(t *testing.T) {
	t.Parallel()
	authorizer := OwnerClaimAuthorizer("hadron-local", "janitor")
	access := testAccess()
	access.Principal = "system"
	cleanup := values.ArtifactAuthorization{Operation: values.ArtifactOperationCleanup, Access: access}
	if err := authorizer.AuthorizeArtifact(t.Context(), cleanup); !errors.Is(err, values.ErrArtifactUnauthorized) {
		t.Fatalf("default maintenance name must not apply when overridden: %v", err)
	}
	access.Principal = "janitor"
	cleanup.Access = access
	if err := authorizer.AuthorizeArtifact(t.Context(), cleanup); err != nil {
		t.Fatalf("custom maintenance principal: %v", err)
	}
	// Pre-resolution reads of a foreign ref are allowed here; the store
	// re-authorizes against the delegate's returned owner.
	foreign := externalMetadata([]byte("x"), values.RedactionPrivate)
	access.Principal = "worker"
	read := values.ArtifactAuthorization{Operation: values.ArtifactOperationStat, Access: access, Ref: &foreign.Ref}
	if err := authorizer.AuthorizeArtifact(t.Context(), read); err != nil {
		t.Fatalf("foreign pre-resolution Stat: %v", err)
	}
	read.Owner = &foreign.Owner
	access.ProjectID = "another-project"
	read.Access = access
	if err := authorizer.AuthorizeArtifact(t.Context(), read); !errors.Is(err, values.ErrArtifactUnauthorized) {
		t.Fatalf("foreign owner mismatch: %v", err)
	}
	// Invalid access is rejected before any claim is considered.
	if err := authorizer.AuthorizeArtifact(t.Context(), values.ArtifactAuthorization{Operation: values.ArtifactOperationStat}); err == nil {
		t.Fatal("empty access was accepted")
	}
}
