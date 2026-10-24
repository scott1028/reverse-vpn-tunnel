package config

import (
	"os"
	"time"
)

type fileSig struct {
	mtime time.Time
	size  int64
	ok    bool
}

func statSig(path string) fileSig {
	st, err := os.Stat(path)
	if err != nil {
		return fileSig{}
	}
	return fileSig{mtime: st.ModTime(), size: st.Size(), ok: true}
}

// Watcher detects saved edits by polling mtime + size. Stat by path also
// covers editors that save by rename (vim).
type Watcher struct {
	path       string
	applied    fileSig
	pending    fileSig
	hasPending bool
}

// NewWatcher starts with nothing applied; LoadServer marks what was read.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path}
}

// LoadServer reads the server config and marks exactly that read as applied,
// so an edit made after it is reported by Changed. A read that fails to
// parse is marked too: the next edit triggers the next attempt.
func (w *Watcher) LoadServer() (Server, error) {
	cfg, sig, err := loadServer(w.path)
	w.applied, w.hasPending = sig, false
	return cfg, err
}

// Changed reports a change once the file looks the same on two polls in a
// row, so a truncate-then-write save (nano) is not read half written.
func (w *Watcher) Changed() bool {
	sig := statSig(w.path)
	if sig == w.applied {
		w.hasPending = false
		return false
	}
	if !w.hasPending || sig != w.pending {
		w.pending, w.hasPending = sig, true
		return false
	}
	w.applied, w.hasPending = sig, false
	return true
}
