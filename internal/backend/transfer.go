package backend

import (
	"context"
	"fmt"
)

// Transferer is implemented by a DESTINATION backend that can ingest an object
// directly from a source node — node-to-node — WITHOUT the bytes being relayed
// through the orchestrating client process. This is the move-transport contract
// (D8/D11): the client drives the operation but never becomes a byte relay, and
// there is deliberately no generic stream-through-the-client fallback. A backend
// pairing with no peer-to-peer path is an explicit error, never a silent relay
// (single-home + never-silent-success).
type Transferer interface {
	// TransferFrom copies the object at key from src into this backend's store.
	// The write obeys the same atomic temp+rename + content-addressed-dedup
	// contract as Put; a missing source object surfaces as TV-OBJ-01. src is the
	// backend reading the source node's vault; an unsupported source kind is an
	// error (never a silent relay).
	TransferFrom(ctx context.Context, src Backend, key string) error
}

// Transfer copies one object from src to dest directly, node-to-node. The
// destination must implement Transferer (SSH→node rsync/ssh, Taildrive→Taildrive
// root-to-root copy through the mounts). There is no client-relay fallback: a
// destination backend that cannot receive a peer-to-peer transfer is a hard
// error, so a move never silently streams the bytes through the client (D8).
func Transfer(ctx context.Context, src, dest Backend, key string) error {
	t, ok := dest.(Transferer)
	if !ok {
		return fmt.Errorf("backend: destination %T cannot receive a node-to-node transfer", dest)
	}
	return t.TransferFrom(ctx, src, key)
}
