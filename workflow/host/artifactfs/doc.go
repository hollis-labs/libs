// Package artifactfs is a crash-safe, local-filesystem implementation of
// go-workflow's values.ArtifactStore.
//
// Layout. Every artifact lives in a content-bound directory
// objects/<scope>/<sha256(ownerID)>/<artifactID>/ holding a "payload" file and
// a strictly decoded "manifest.json"; in-flight writes live under staging/
// (prefix "partial-"). Directories are mode 0700 and files 0600. Owner ids and
// absolute paths never enter a reference: the URI is
// artifact://<authority>/<scope>/<sha256(ownerID)>/<artifactID>, where the
// artifact id is the SHA-256 of the artifact's immutable identity (authority,
// owner scope and hash, digest, media type, size, producer, redaction,
// retention, timestamps).
//
// Durability. Put streams the source through a size bound and SHA-256 into
// staging, fsyncs payload, manifest and directory, then publishes the
// directory with one atomic rename; identical concurrent Puts converge on one
// artifact. Reads verify the complete payload digest before releasing bytes.
//
// Fail-closed checks. The identities of the root, objects and staging
// directories are captured by New and revalidated on every operation, symlinks
// and non-regular files are rejected on every path component, and
// authorization happens before any local path is resolved and again after the
// manifest is strictly loaded. The authority string is persisted identity: it
// is required, has no default, and must never change for an existing root.
//
// The on-disk format is frozen; changing it requires a manifest version bump.
// References of other authorities are routed only to delegates approved with
// WithExternal and are never deleted through them.
package artifactfs
