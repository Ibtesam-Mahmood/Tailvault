package wal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Ibtesam-Mahmood/tailvault/internal/backend"
	toml "github.com/pelletier/go-toml/v2"
)

// Typed sentinels so the command boundary can map to tserr. ErrChainBroken →
// TV-FED-03 (exit 6).
var (
	ErrOpInFlight  = errors.New("wal: op in flight on blob")
	ErrChainBroken = errors.New("wal: hash chain verification failed")
	ErrDuplicateOp = errors.New("wal: op id already recorded")
)

const (
	walPrefix = "meta/wal/"
	// prunedPrefix holds forward-only anchor markers (meta/wal/pruned/<seq12>).
	// The effective anchor is the marker with the HIGHEST seq. Advancing the
	// anchor is a single Put of a NEW key (never an overwrite/delete of the live
	// anchor), so a crash during Prune can never leave the chain anchorless and
	// brick it (fix for the non-atomic single-PRUNED-key hazard).
	prunedPrefix = "meta/wal/pruned/"
)

// maxAppendRetries bounds the append-then-verify race loop (a runaway backstop;
// real contention resolves in one or two iterations).
const maxAppendRetries = 256

// Log reads/writes a node's WAL through its backend.Backend (keys under
// meta/wal/). No state is held; every method is a client-driven call.
type Log struct{ B backend.Backend }

// Rec is an entry plus its effective State (derived from sibling markers).
type Rec struct {
	Entry Entry
	State State
}

// anchor is meta/wal/PRUNED: the {seq, hash} of the last pruned entry, so the
// surviving chain still verifies after journal gc.
type anchor struct {
	Seq  uint64 `toml:"seq"`
	Hash string `toml:"hash"`
}

// marker is one sibling .done/.failed file (SPEC v2 §10 / DG-27.2). It is keyed
// by seq (the slot is unique), with the op id carried for cross-reference.
type marker struct {
	Seq    uint64    `toml:"seq"`
	OpID   string    `toml:"op_id"`
	State  string    `toml:"state"`
	At     time.Time `toml:"at"`
	Reason string    `toml:"reason,omitempty"`
}

// entryFile bundles an on-disk entry with its raw bytes, self-hash and state.
type entryFile struct {
	key      string
	raw      []byte
	entry    Entry
	selfHash string
	state    State
}

// entryKey is the slot key. The op id lives INSIDE the file, not in the name, so
// that two clients racing the same seq write the SAME key — backend Put dedup
// then makes the first write stick (the slot file IS the lock claim). markers
// are keyed by seq for the same reason (DG-29.1; see SPEC v2 §10).
func entryKey(seq uint64) string {
	return fmt.Sprintf("%s%012d.toml", walPrefix, seq)
}

func markerKey(seq uint64, suffix string) string {
	return fmt.Sprintf("%s%012d.%s", walPrefix, seq, suffix)
}

func anchorKey(seq uint64) string {
	return fmt.Sprintf("%s%012d", prunedPrefix, seq)
}

// loadRaw reads every WAL file, decoding entries, markers and the PRUNED anchor.
// It does NOT verify the chain or filter by anchor — that is verify's job. A key
// that vanishes between List and Get (a benign TOCTOU under concurrent Prune) is
// skipped.
func (l *Log) loadRaw(ctx context.Context) ([]entryFile, *anchor, error) {
	keys, err := l.B.List(ctx, walPrefix)
	if err != nil {
		return nil, nil, err
	}
	var anch *anchor
	markersBySeq := map[uint64]State{}
	var files []entryFile

	for _, key := range keys {
		name := strings.TrimPrefix(key, walPrefix)
		switch {
		case strings.HasPrefix(name, "pruned/"):
			// Forward-only anchor marker; the effective anchor is the highest seq.
			b, gone, err := l.getMaybe(ctx, key)
			if err != nil {
				return nil, nil, err
			}
			if gone {
				continue
			}
			var a anchor
			if err := toml.Unmarshal(b, &a); err != nil {
				return nil, nil, fmt.Errorf("wal: decode anchor %s: %w", name, err)
			}
			if anch == nil || a.Seq > anch.Seq {
				cp := a
				anch = &cp
			}
		case strings.HasSuffix(name, ".toml"):
			b, gone, err := l.getMaybe(ctx, key)
			if err != nil {
				return nil, nil, err
			}
			if gone {
				continue
			}
			e, err := Decode(b)
			if err != nil {
				return nil, nil, err
			}
			files = append(files, entryFile{key: key, raw: b, entry: e, selfHash: hashBytes(b)})
		case strings.HasSuffix(name, ".done"):
			if seq, ok := seqFromMarker(name, ".done"); ok {
				markersBySeq[seq] = StateDone
			}
		case strings.HasSuffix(name, ".failed"):
			if seq, ok := seqFromMarker(name, ".failed"); ok {
				if _, done := markersBySeq[seq]; !done {
					markersBySeq[seq] = StateFailed
				}
			}
		}
	}
	for i := range files {
		if st, ok := markersBySeq[files[i].entry.Seq]; ok {
			files[i].state = st
		} else {
			files[i].state = StateIntent
		}
	}
	return files, anch, nil
}

// seqFromMarker parses the seq from a marker filename "<seq12>.<suffix>".
func seqFromMarker(name, suffix string) (uint64, bool) {
	base := strings.TrimSuffix(name, suffix)
	if len(base) != 12 {
		return 0, false
	}
	var v uint64
	for i := 0; i < 12; i++ {
		c := base[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return v, true
}

// readVerified loads, drops pruning leftovers (entries at or below the anchor),
// orders by seq, and verifies the hash chain. Any break returns ErrChainBroken.
func (l *Log) readVerified(ctx context.Context) ([]entryFile, *anchor, error) {
	files, anch, err := l.loadRaw(ctx)
	if err != nil {
		return nil, nil, err
	}
	if anch != nil {
		kept := files[:0]
		for _, f := range files {
			if f.entry.Seq > anch.Seq {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	sort.Slice(files, func(i, j int) bool { return files[i].entry.Seq < files[j].entry.Seq })
	if err := verifyChain(files, anch); err != nil {
		return nil, nil, err
	}
	return files, anch, nil
}

// verifyChain checks seq contiguity and prev_hash linkage end to end. The first
// survivor anchors to PRUNED (if present) else to ZeroHash at seq 0.
func verifyChain(ordered []entryFile, anch *anchor) error {
	if len(ordered) == 0 {
		return nil
	}
	first := ordered[0]
	if anch != nil {
		if first.entry.Seq != anch.Seq+1 || first.entry.PrevHash != anch.Hash {
			return fmt.Errorf("%w: first survivor seq=%d prev=%s does not anchor to PRUNED seq=%d hash=%s",
				ErrChainBroken, first.entry.Seq, first.entry.PrevHash, anch.Seq, anch.Hash)
		}
	} else {
		if first.entry.Seq != 0 || first.entry.PrevHash != ZeroHash {
			return fmt.Errorf("%w: genesis must be seq 0 with zero prev_hash, got seq=%d prev=%s",
				ErrChainBroken, first.entry.Seq, first.entry.PrevHash)
		}
	}
	for i := 1; i < len(ordered); i++ {
		prev, cur := ordered[i-1], ordered[i]
		if cur.entry.Seq != prev.entry.Seq+1 {
			return fmt.Errorf("%w: seq gap %d -> %d", ErrChainBroken, prev.entry.Seq, cur.entry.Seq)
		}
		if cur.entry.PrevHash != prev.selfHash {
			return fmt.Errorf("%w: seq %d prev_hash %s != hash(seq %d) %s",
				ErrChainBroken, cur.entry.Seq, cur.entry.PrevHash, prev.entry.Seq, prev.selfHash)
		}
	}
	return nil
}

// Read lists, decodes, orders by seq and VERIFIES THE HASH CHAIN; any broken
// link returns ErrChainBroken. Always verify-on-read — never trust blindly.
func (l *Log) Read(ctx context.Context) ([]Rec, error) {
	files, _, err := l.readVerified(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]Rec, len(files))
	for i, f := range files {
		recs[i] = Rec{Entry: f.entry, State: f.state}
	}
	return recs, nil
}

// AppendIntent is WAL-as-lock. It verifies the chain, rejects a duplicate op id
// (ErrDuplicateOp + the existing record), rejects an op whose blob refs collide
// with a pending intent (ErrOpInFlight), then writes the next-seq slot with
// prev_hash = hash(tail). The slot file is the lock claim: backend Put dedup
// makes the first writer stick, so a loser reads back a different op id and
// retries at the next seq (a same-blob loser is caught by the pending-intent
// check on the retry and returns ErrOpInFlight).
func (l *Log) AppendIntent(ctx context.Context, e Entry) (Rec, error) {
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		files, anch, err := l.readVerified(ctx)
		if err != nil {
			return Rec{}, err
		}

		// Idempotency: an op id already present (any state) is a detected no-op.
		for _, f := range files {
			if f.entry.OpID == e.OpID {
				return Rec{Entry: f.entry, State: f.state}, ErrDuplicateOp
			}
		}

		// Per-blob lock: a pending intent sharing a blob blocks us.
		for _, f := range files {
			if f.state == StateIntent && sharesBlob(f.entry.BlobRefs, e.BlobRefs) {
				return Rec{Entry: f.entry, State: StateIntent}, ErrOpInFlight
			}
		}

		// Compute the next slot.
		var nextSeq uint64
		var prevHash string
		switch {
		case len(files) > 0:
			tail := files[len(files)-1]
			nextSeq = tail.entry.Seq + 1
			prevHash = tail.selfHash
		case anch != nil:
			nextSeq = anch.Seq + 1
			prevHash = anch.Hash
		default:
			nextSeq = 0
			prevHash = ZeroHash
		}

		cand := e
		cand.Seq = nextSeq
		cand.PrevHash = prevHash
		if cand.CreatedAt.IsZero() {
			cand.CreatedAt = time.Now().UTC()
		}
		b, err := Encode(cand)
		if err != nil {
			return Rec{}, err
		}
		// Put the slot. If it already exists, dedup makes this a no-op and the
		// existing (winner's) content stays.
		if err := l.B.Put(ctx, entryKey(nextSeq), bytes.NewReader(b)); err != nil {
			return Rec{}, err
		}

		// Read the slot back: did our op id stick?
		got, gone, err := l.getMaybe(ctx, entryKey(nextSeq))
		if err != nil {
			return Rec{}, err
		}
		if gone {
			continue // pruned concurrently; retry
		}
		owner, err := Decode(got)
		if err != nil {
			return Rec{}, err
		}
		if owner.OpID == cand.OpID {
			return Rec{Entry: cand, State: StateIntent}, nil
		}
		// We lost this slot to another writer; retry at the next seq. A same-blob
		// loser will hit the pending-intent check above on the next iteration.
	}
	return Rec{}, fmt.Errorf("wal: append did not converge after %d attempts", maxAppendRetries)
}

// MarkDone writes the sibling .done marker (idempotent; the entry is never
// edited). A double MarkDone, or MarkDone of an unknown op, is a silent success.
func (l *Log) MarkDone(ctx context.Context, opID string) error {
	return l.mark(ctx, opID, "done", StateDone, "")
}

// MarkFailed writes the sibling .failed marker with a reason.
func (l *Log) MarkFailed(ctx context.Context, opID, reason string) error {
	return l.mark(ctx, opID, "failed", StateFailed, reason)
}

func (l *Log) mark(ctx context.Context, opID, suffix string, state State, reason string) error {
	files, _, err := l.readVerified(ctx)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.entry.OpID == opID {
			key := markerKey(f.entry.Seq, suffix)
			// Put dedups on an existing key, so re-marking is a silent no-op.
			if st, _ := l.B.Stat(ctx, key); st.Exists {
				return nil
			}
			m := marker{Seq: f.entry.Seq, OpID: opID, State: string(state), At: time.Now().UTC(), Reason: reason}
			b, err := toml.Marshal(m)
			if err != nil {
				return fmt.Errorf("wal: encode marker: %w", err)
			}
			return l.B.Put(ctx, key, bytes.NewReader(b))
		}
	}
	return nil // unknown op id: silent success (idempotent)
}

// Pending returns intent-state entries, optionally filtered to a blob ref —
// gc's skip set (D13) and the ops command's list source.
func (l *Log) Pending(ctx context.Context, blobRef string) ([]Rec, error) {
	recs, err := l.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out []Rec
	for _, r := range recs {
		if r.State != StateIntent {
			continue
		}
		if blobRef != "" && !containsStr(r.Entry.BlobRefs, blobRef) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Prune (journal gc) deletes the leading run of done entries older than keep,
// recording the last pruned entry's {seq, hash} in meta/wal/PRUNED so the
// surviving chain still verifies. It never prunes intent/failed entries, and
// stops at the first entry that is not both done and old (so it only ever
// removes a contiguous done prefix). Returns the number of entries pruned.
func (l *Log) Prune(ctx context.Context, keep time.Duration) (int, error) {
	files, prevAnchor, err := l.readVerified(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-keep)
	var toPrune []entryFile
	for _, f := range files {
		if f.state == StateDone && f.entry.CreatedAt.Before(cutoff) {
			toPrune = append(toPrune, f)
			continue
		}
		break // stop at the first non-(done & old) entry
	}
	if len(toPrune) == 0 {
		return 0, nil
	}
	last := toPrune[len(toPrune)-1]

	// Advance the anchor by Put-ing a NEW forward-only marker (meta/wal/pruned/
	// <seq>). This is the FIRST mutation and is crash-atomic: the live anchor is
	// never deleted before its successor exists, so a crash here can never leave
	// the chain anchorless (the old anchor — or genesis — still verifies). A
	// reader takes the highest-seq anchor and ignores entries at/below it.
	a := anchor{Seq: last.entry.Seq, Hash: last.selfHash}
	ab, err := toml.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("wal: encode anchor: %w", err)
	}
	if err := l.B.Put(ctx, anchorKey(last.entry.Seq), bytes.NewReader(ab)); err != nil {
		return 0, err
	}
	// Now delete the pruned entries + their done markers (idempotent; a crash
	// mid-loop just leaves ignorable below-anchor entries for the next run).
	for _, f := range toPrune {
		if err := l.B.Delete(ctx, f.key); err != nil {
			return 0, err
		}
		_ = l.B.Delete(ctx, markerKey(f.entry.Seq, "done"))
	}
	// Best-effort: drop the superseded anchor marker (safe — readers take the max,
	// and the new anchor is already durable). Never deletes the live one.
	if prevAnchor != nil && prevAnchor.Seq < last.entry.Seq {
		_ = l.B.Delete(ctx, anchorKey(prevAnchor.Seq))
	}
	return len(toPrune), nil
}

// getMaybe reads a key, reporting gone=true (instead of an error) when the key
// has vanished since List returned it — a benign TOCTOU under concurrent Prune.
// Other errors propagate.
func (l *Log) getMaybe(ctx context.Context, key string) (b []byte, gone bool, err error) {
	var buf bytes.Buffer
	if err := l.B.Get(ctx, key, &buf); err != nil {
		if errors.Is(err, backend.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return buf.Bytes(), false, nil
}

func sharesBlob(a, b []string) bool {
	for _, x := range a {
		if containsStr(b, x) {
			return true
		}
	}
	return false
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
