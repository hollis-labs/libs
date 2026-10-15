// Package atomicfile provides crash-safe filesystem primitives.
//
// WriteFile and NewWriter write to a sibling temp file, fsync, then
// os.Rename over the destination. The rename is atomic on POSIX so readers
// never observe a partial or truncated file. After the rename the parent
// directory is fsynced so the new directory entry is durable across power
// loss (a no-op on Windows).
//
// Both primitives preserve the requested mode on the final file and remove
// the temp file on any error path, including partial writes.
package atomicfile
