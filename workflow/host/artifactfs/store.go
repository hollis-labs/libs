package artifactfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/libs/workflow/values"
)

// Store is a local, immutable go-workflow artifact store. Each artifact is
// stored as a content-bound directory containing a payload and a strictly
// decoded manifest. Owner identities are hashed before they enter a path or
// URI. References of other authorities are routed only to delegates approved
// with WithExternal and are never deleted through them.
type Store struct {
	root            string
	rootIdentity    os.FileInfo
	objectsIdentity os.FileInfo
	stagingIdentity os.FileInfo
	authority       string
	authorizer      values.ArtifactAuthorizer
	externals       map[string]values.ArtifactStore
}

// Option configures New.
type Option func(*config)

type config struct {
	externals []externalAuthority
}

type externalAuthority struct {
	authority string
	delegate  values.ArtifactStore
}

// WithExternal approves a foreign authority: Stat and Open of references whose
// Store is authority are passed to delegate (after this store's own
// authorization, and re-authorized against the metadata the delegate returns),
// and Delete or Cleanup of such references preserves them without calling the
// delegate. The authority must be canonical and differ from the store's own.
func WithExternal(authority string, delegate values.ArtifactStore) Option {
	return func(cfg *config) {
		cfg.externals = append(cfg.externals, externalAuthority{authority: authority, delegate: delegate})
	}
}

// New constructs a fail-closed store below root. authority is the persisted
// identity of the store: it is part of every ArtifactRef (Store field and URI
// host) and of every artifact id, so it has no default and must never change
// for an existing root. It must match ^[a-z][a-z0-9.-]{0,62}$. authorizer is
// the host's authorization boundary and is required.
func New(root, authority string, authorizer values.ArtifactAuthorizer, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, values.ErrArtifactInvalid)
	}
	if !canonicalAuthority(authority) {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureAuthority, nil, values.ErrArtifactAuthority)
	}
	if authorizer == nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureAuthority, nil, values.ErrArtifactAuthority)
	}
	var cfg config
	for _, option := range opts {
		if option != nil {
			option(&cfg)
		}
	}
	externals := make(map[string]values.ArtifactStore, len(cfg.externals))
	for _, external := range cfg.externals {
		if _, duplicate := externals[external.authority]; duplicate || !canonicalAuthority(external.authority) ||
			external.authority == authority || external.delegate == nil {
			return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureAuthority, nil, values.ErrArtifactAuthority)
		}
		externals[external.authority] = external.delegate
	}
	absolute, absoluteErr := filepath.Abs(root)
	if absoluteErr != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, absoluteErr)
	}
	if err := rejectArtifactSymlinkComponents(absolute); err != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, err)
	}
	if err := rejectArtifactSymlinkComponents(absolute); err != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, err)
	}
	rootInfo, rootErr := os.Lstat(absolute)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, values.ErrArtifactInvalid)
	}
	resolved, resolveErr := filepath.EvalSymlinks(absolute)
	if resolveErr != nil || resolved != absolute {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, values.ErrArtifactInvalid)
	}
	if err := os.Chmod(resolved, 0o700); err != nil { // #nosec G302 -- the artifact root is intentionally owner-only.
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, err)
	}
	for _, component := range []string{"objects", "staging"} {
		if _, err := ensureArtifactDirectoryChain(resolved, component); err != nil {
			return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, err)
		}
	}
	rootIdentity, rootIdentityErr := os.Lstat(resolved)
	if rootIdentityErr != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, rootIdentityErr)
	}
	objectsIdentity, objectsIdentityErr := os.Lstat(filepath.Join(resolved, "objects"))
	if objectsIdentityErr != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, objectsIdentityErr)
	}
	stagingIdentity, stagingIdentityErr := os.Lstat(filepath.Join(resolved, "staging"))
	if stagingIdentityErr != nil {
		return nil, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, nil, stagingIdentityErr)
	}
	return &Store{
		root: resolved, rootIdentity: rootIdentity,
		objectsIdentity: objectsIdentity, stagingIdentity: stagingIdentity,
		authority: authority, authorizer: authorizer, externals: externals,
	}, nil
}

// Put streams one bounded artifact into an atomic staging directory before
// publishing its immutable content-bound location.
func (store *Store) Put(ctx context.Context, request values.ArtifactPutRequest, source io.Reader) (values.ArtifactMetadata, error) {
	request = snapshotArtifactPutRequest(request)
	if err := checkArtifactContext(ctx, values.ArtifactOperationPut, nil); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if err := request.Validate(); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if !canonicalAuthority(request.Store) {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureAuthority, nil, values.ErrArtifactAuthority)
	}
	if err := store.authorize(ctx, values.ArtifactOperationPut, request.Access, nil, &request.Owner); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if request.Store != store.authority {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureAuthority, nil, values.ErrArtifactAuthority)
	}
	if source == nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, values.ErrArtifactInvalid)
	}
	if err := store.validateRoots(); err != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, err)
	}
	return store.putLocal(ctx, request, source)
}

func (store *Store) putLocal(ctx context.Context, request values.ArtifactPutRequest, source io.Reader) (metadata values.ArtifactMetadata, resultErr error) {
	stagingRoot := filepath.Join(store.root, "staging")
	stage, stageErr := os.MkdirTemp(stagingRoot, "partial-")
	if stageErr != nil {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, stageErr)
	}
	if err := os.Chmod(stage, 0o700); err != nil { // #nosec G302 -- staging is intentionally owner-only.
		_ = os.RemoveAll(stage)
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, err)
	}
	defer func() {
		if resultErr != nil || stage != "" {
			_ = os.RemoveAll(stage)
		}
	}()

	payloadPath := filepath.Join(stage, artifactPayloadName)
	payload, payloadErr := os.OpenFile(payloadPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- stage is generated below an identity-checked root.
	if payloadErr != nil {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, payloadErr)
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: artifactContextReader{ctx: ctx, reader: source}, N: request.MaxBytes + 1}
	size, copyErr := io.Copy(io.MultiWriter(payload, hasher), limited)
	if copyErr == nil {
		copyErr = payload.Sync()
	}
	closeErr := payload.Close()
	if copyErr != nil {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, copyErr)
	}
	if closeErr != nil {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, closeErr)
	}
	if err := checkArtifactContext(ctx, values.ArtifactOperationPut, nil); err != nil {
		return metadata, err
	}
	if size > request.MaxBytes {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureSize, nil, values.ErrArtifactSizeLimit)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if request.ExpectedDigest != "" && request.ExpectedDigest != digest {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureDigest, nil, values.ErrArtifactDigest)
	}
	if request.ExpectedSize != nil && *request.ExpectedSize != size {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureDigest, nil, values.ErrArtifactDigest)
	}

	createdAt := request.CreatedAt.UTC().Round(0)
	expiresAt := request.ExpiresAt
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC().Round(0)
	}
	ownerHash := artifactOwnerHash(request.Owner.ID)
	identity := artifactIdentity{
		Store: store.authority, OwnerScope: request.Owner.Scope, OwnerHash: ownerHash,
		Digest: digest, MediaType: request.Metadata.MediaType, SizeBytes: size,
		Producer: request.Metadata.Producer, Redaction: request.Metadata.Redaction,
		Retention: request.Metadata.Retention, CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
	id, identityErr := artifactIDFor(identity)
	if identityErr != nil {
		return metadata, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, nil, identityErr)
	}
	locator := artifactLocator{scope: request.Owner.Scope, ownerHash: ownerHash, artifactID: id}
	ref := values.ArtifactRef{
		Store: store.authority, URI: artifactURI(store.authority, locator), Digest: digest,
		MediaType: request.Metadata.MediaType, SizeBytes: size, Producer: request.Metadata.Producer,
		Redaction: request.Metadata.Redaction, Retention: request.Metadata.Retention,
	}
	metadata = values.ArtifactMetadata{Ref: ref, Owner: request.Owner, CreatedAt: createdAt, ExpiresAt: expiresAt}
	manifest, manifestErr := encodeArtifactManifest(metadata)
	if manifestErr != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, manifestErr)
	}
	manifestFile, manifestOpenErr := os.OpenFile(filepath.Join(stage, artifactManifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- stage is generated below an identity-checked root.
	if manifestOpenErr != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, manifestOpenErr)
	}
	_, writeErr := manifestFile.Write(manifest)
	if writeErr == nil {
		writeErr = manifestFile.Sync()
	}
	manifestCloseErr := manifestFile.Close()
	if writeErr != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, writeErr)
	}
	if manifestCloseErr != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, manifestCloseErr)
	}
	if err := syncArtifactDirectory(stage); err != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, err)
	}
	if err := checkArtifactContext(ctx, values.ArtifactOperationPut, &ref); err != nil {
		return values.ArtifactMetadata{}, err
	}

	parent, parentErr := ensureArtifactDirectoryChain(filepath.Join(store.root, "objects"), string(locator.scope), locator.ownerHash)
	if parentErr != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, parentErr)
	}
	final := filepath.Join(parent, locator.artifactID)
	if existing, verifyErr := verifyStoredArtifact(store.authority, final, locator, &ref); verifyErr == nil {
		return existing, nil
	} else if !errors.Is(verifyErr, os.ErrNotExist) {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, verifyErr)
	}
	if err := os.Rename(stage, final); err != nil {
		if existing, verifyErr := verifyStoredArtifact(store.authority, final, locator, &ref); verifyErr == nil {
			return existing, nil
		}
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, err)
	}
	stage = ""
	if err := syncArtifactDirectory(parent); err != nil {
		_ = os.RemoveAll(final)
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationPut, values.ArtifactFailureInvalid, &ref, err)
	}
	return metadata, nil
}

// Stat validates authorization, ownership, expiry, immutable metadata, size,
// and payload digest before returning metadata.
func (store *Store) Stat(ctx context.Context, access values.ArtifactAccess, ref values.ArtifactRef) (values.ArtifactMetadata, error) {
	if err := store.prepareResolve(ctx, values.ArtifactOperationStat, access, ref); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if ref.Store != store.authority {
		return store.statExternal(ctx, access, ref)
	}
	if err := store.validateRoots(); err != nil {
		return values.ArtifactMetadata{}, artifactOperationError(values.ArtifactOperationStat, ref, err)
	}
	return store.statLocal(ctx, values.ArtifactOperationStat, access, ref, true)
}

// Open returns a streaming reader that independently verifies the digest as
// the caller consumes it. The stored bytes are verified once before exposure.
func (store *Store) Open(ctx context.Context, access values.ArtifactAccess, ref values.ArtifactRef) (values.ArtifactReadCloser, error) {
	if err := store.prepareResolve(ctx, values.ArtifactOperationOpen, access, ref); err != nil {
		return nil, err
	}
	if ref.Store != store.authority {
		return store.openExternal(ctx, access, ref)
	}
	if err := store.validateRoots(); err != nil {
		return nil, artifactOperationError(values.ArtifactOperationOpen, ref, err)
	}
	metadata, statErr := store.statLocal(ctx, values.ArtifactOperationOpen, access, ref, false)
	if statErr != nil {
		return nil, statErr
	}
	locator, _ := parseArtifactRef(store.authority, ref)
	directory := store.artifactDirectory(locator)
	if pathErr := rejectArtifactSymlinkComponents(directory); pathErr != nil {
		return nil, artifactOperationError(values.ArtifactOperationOpen, ref, pathErr)
	}
	payload, info, openErr := openArtifactRegularFile(filepath.Join(directory, artifactPayloadName), metadata.Ref.SizeBytes)
	if openErr != nil {
		return nil, artifactOperationError(values.ArtifactOperationOpen, ref, openErr)
	}
	if info.Size() != metadata.Ref.SizeBytes {
		_ = payload.Close()
		return nil, artifactError(values.ArtifactOperationOpen, values.ArtifactFailureDigest, &ref, values.ErrArtifactDigest)
	}
	if err := verifyArtifactPayload(ctx, values.ArtifactOperationOpen, payload, metadata.Ref); err != nil {
		_ = payload.Close()
		return nil, err
	}
	reader, readerErr := values.NewVerifyingArtifactReader(metadata, payload)
	if readerErr != nil {
		_ = payload.Close()
		return nil, readerErr
	}
	return reader, nil
}

func (store *Store) prepareResolve(ctx context.Context, operation values.ArtifactOperation, access values.ArtifactAccess, ref values.ArtifactRef) error {
	if err := checkArtifactContext(ctx, operation, &ref); err != nil {
		return err
	}
	if err := store.validateRef(ref); err != nil {
		return artifactOperationError(operation, ref, err)
	}
	// This first check deliberately precedes root validation, delegate routing
	// and local path construction. Owner-aware authorization follows strict
	// manifest loading (local) or the delegate's metadata (external).
	return store.authorize(ctx, operation, access, &ref, nil)
}

func (store *Store) statLocal(ctx context.Context, operation values.ArtifactOperation, access values.ArtifactAccess, ref values.ArtifactRef, verifyDigest bool) (values.ArtifactMetadata, error) {
	locator, err := parseArtifactRef(store.authority, ref)
	if err != nil {
		return values.ArtifactMetadata{}, artifactOperationError(operation, ref, err)
	}
	directory := store.artifactDirectory(locator)
	if pathErr := rejectArtifactSymlinkComponents(directory); pathErr != nil {
		return values.ArtifactMetadata{}, artifactOperationError(operation, ref, pathErr)
	}
	metadata, err := verifyStoredArtifact(store.authority, directory, locator, &ref)
	if err != nil {
		return values.ArtifactMetadata{}, artifactOperationError(operation, ref, err)
	}
	if err := store.authorize(ctx, operation, access, &ref, &metadata.Owner); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if err := values.CheckArtifactExpiry(operation, metadata, access.At); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if verifyDigest {
		payload, _, openErr := openArtifactRegularFile(filepath.Join(directory, artifactPayloadName), metadata.Ref.SizeBytes)
		if openErr != nil {
			return values.ArtifactMetadata{}, artifactOperationError(operation, ref, openErr)
		}
		verifyErr := verifyArtifactPayload(ctx, operation, payload, metadata.Ref)
		closeErr := payload.Close()
		if verifyErr != nil {
			return values.ArtifactMetadata{}, verifyErr
		}
		if closeErr != nil {
			return values.ArtifactMetadata{}, artifactOperationError(operation, ref, closeErr)
		}
	}
	return metadata, nil
}

// validateRef accepts a well-formed reference of the local authority or an
// opaque reference of any canonical foreign authority. Foreign URIs belong to
// their delegate's grammar and are validated only by ArtifactRef.Validate.
func (store *Store) validateRef(ref values.ArtifactRef) error {
	if err := ref.Validate(); err != nil || !canonicalAuthority(ref.Store) {
		return values.ErrArtifactInvalid
	}
	if ref.Store != store.authority {
		return nil
	}
	return validateArtifactRef(store.authority, ref)
}

func (store *Store) statExternal(ctx context.Context, access values.ArtifactAccess, ref values.ArtifactRef) (values.ArtifactMetadata, error) {
	delegate, ok := store.externals[ref.Store]
	if !ok {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationStat, values.ArtifactFailureAuthority, &ref, values.ErrArtifactAuthority)
	}
	metadata, err := delegate.Stat(ctx, access, ref)
	if err != nil {
		return values.ArtifactMetadata{}, artifactOperationError(values.ArtifactOperationStat, ref, err)
	}
	if metadata.Ref != ref || metadata.Ref.Retention != values.RetentionExternal || metadata.Validate() != nil {
		return values.ArtifactMetadata{}, artifactError(values.ArtifactOperationStat, values.ArtifactFailureInvalid, &ref, values.ErrArtifactInvalid)
	}
	if err := store.authorize(ctx, values.ArtifactOperationStat, access, &ref, &metadata.Owner); err != nil {
		return values.ArtifactMetadata{}, err
	}
	if err := values.CheckArtifactExpiry(values.ArtifactOperationStat, metadata, access.At); err != nil {
		return values.ArtifactMetadata{}, err
	}
	return metadata, nil
}

func (store *Store) openExternal(ctx context.Context, access values.ArtifactAccess, ref values.ArtifactRef) (values.ArtifactReadCloser, error) {
	delegate, ok := store.externals[ref.Store]
	if !ok {
		return nil, artifactError(values.ArtifactOperationOpen, values.ArtifactFailureAuthority, &ref, values.ErrArtifactAuthority)
	}
	reader, err := delegate.Open(ctx, access, ref)
	if err != nil {
		return nil, artifactOperationError(values.ArtifactOperationOpen, ref, err)
	}
	if reader == nil {
		return nil, artifactError(values.ArtifactOperationOpen, values.ArtifactFailureInvalid, &ref, values.ErrArtifactInvalid)
	}
	metadata := reader.Metadata()
	if metadata.Ref != ref || metadata.Ref.Retention != values.RetentionExternal || metadata.Validate() != nil {
		_ = reader.Close()
		return nil, artifactError(values.ArtifactOperationOpen, values.ArtifactFailureInvalid, &ref, values.ErrArtifactInvalid)
	}
	if err := store.authorize(ctx, values.ArtifactOperationOpen, access, &ref, &metadata.Owner); err != nil {
		_ = reader.Close()
		return nil, err
	}
	if err := values.CheckArtifactExpiry(values.ArtifactOperationOpen, metadata, access.At); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}
