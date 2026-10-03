package artifactfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/hollis-labs/libs/workflow/values"
)

const manifestVersion = 1

type storedManifest struct {
	Version  int                     `json:"version"`
	Metadata values.ArtifactMetadata `json:"metadata"`
}

type artifactIdentity struct {
	Store      string                    `json:"store"`
	OwnerScope values.ArtifactOwnerScope `json:"owner_scope"`
	OwnerHash  string                    `json:"owner_hash"`
	Digest     string                    `json:"digest"`
	MediaType  string                    `json:"media_type"`
	SizeBytes  int64                     `json:"size_bytes"`
	Producer   values.Producer           `json:"producer"`
	Redaction  values.RedactionClass     `json:"redaction"`
	Retention  values.RetentionClass     `json:"retention"`
	CreatedAt  time.Time                 `json:"created_at"`
	ExpiresAt  time.Time                 `json:"expires_at,omitempty"`
}

func artifactIDFor(identity artifactIdentity) (string, error) {
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func encodeArtifactManifest(metadata values.ArtifactMetadata) ([]byte, error) {
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(storedManifest{Version: manifestVersion, Metadata: metadata})
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeArtifactManifest(content []byte) (storedManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest storedManifest
	if err := decoder.Decode(&manifest); err != nil {
		return storedManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return storedManifest{}, values.ErrArtifactInvalid
		}
		return storedManifest{}, err
	}
	if manifest.Version != manifestVersion {
		return storedManifest{}, values.ErrArtifactInvalid
	}
	if err := manifest.Metadata.Validate(); err != nil {
		return storedManifest{}, err
	}
	return manifest, nil
}

func readArtifactManifest(directory string) (storedManifest, error) {
	file, info, err := openArtifactRegularFile(filepath.Join(directory, artifactManifestName), 1<<20)
	if err != nil {
		return storedManifest{}, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
	closeErr := file.Close()
	if readErr != nil {
		return storedManifest{}, readErr
	}
	if closeErr != nil {
		return storedManifest{}, closeErr
	}
	if int64(len(content)) != info.Size() {
		return storedManifest{}, values.ErrArtifactInvalid
	}
	return decodeArtifactManifest(content)
}

func verifyStoredArtifact(authority string, directory string, locator artifactLocator, expected *values.ArtifactRef) (values.ArtifactMetadata, error) {
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return values.ArtifactMetadata{}, err
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return values.ArtifactMetadata{}, values.ErrArtifactInvalid
	}
	manifest, err := readArtifactManifest(directory)
	if err != nil {
		return values.ArtifactMetadata{}, err
	}
	metadata := manifest.Metadata
	if metadata.Ref.Store != authority || metadata.Ref.URI != artifactURI(authority, locator) ||
		metadata.Owner.Scope != locator.scope || artifactOwnerHash(metadata.Owner.ID) != locator.ownerHash {
		return values.ArtifactMetadata{}, values.ErrArtifactInvalid
	}
	identity := artifactIdentity{
		Store: authority, OwnerScope: metadata.Owner.Scope, OwnerHash: locator.ownerHash,
		Digest: metadata.Ref.Digest, MediaType: metadata.Ref.MediaType, SizeBytes: metadata.Ref.SizeBytes,
		Producer: metadata.Ref.Producer, Redaction: metadata.Ref.Redaction, Retention: metadata.Ref.Retention,
		CreatedAt: metadata.CreatedAt, ExpiresAt: metadata.ExpiresAt,
	}
	computedID, err := artifactIDFor(identity)
	if err != nil || computedID != locator.artifactID {
		return values.ArtifactMetadata{}, values.ErrArtifactInvalid
	}
	if expected != nil && !reflect.DeepEqual(metadata.Ref, *expected) {
		return values.ArtifactMetadata{}, values.ErrArtifactInvalid
	}
	payloadInfo, err := requireArtifactRegularFile(filepath.Join(directory, artifactPayloadName))
	if err != nil {
		return values.ArtifactMetadata{}, err
	}
	if payloadInfo.Size() != metadata.Ref.SizeBytes {
		return values.ArtifactMetadata{}, values.ErrArtifactDigest
	}
	return metadata, nil
}
