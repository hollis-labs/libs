// Package pipeline runs ordered, bounded message annotation stages.
// It owns neither message delivery nor external effects. Applications provide
// durable result storage and atomically settle their own sink receipts/cursors.
package pipeline
