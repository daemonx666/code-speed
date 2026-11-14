package source

import "sync"

// Dedup is the bounded set of response keys shared by every file of every
// source: forks and resumes copy history into new files, and a key first
// seen in one file is ignored in any other.
type Dedup struct {
	mu    sync.Mutex
	owner map[string]string // key → the file it was first seen in
	order []string          // FIFO ring for eviction
	next  int
}

// DedupSize is how many keys are remembered.
const DedupSize = 65_536

func NewDedup() *Dedup {
	return &Dedup{owner: make(map[string]string, 1024), order: make([]string, 0, 1024)}
}

// Claim records key for file. It reports whether key belongs to file:
// true when it is new or was first seen in the same file.
func (d *Dedup) Claim(key, file string) (mine, seen bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f, ok := d.owner[key]; ok {
		return f == file, true
	}
	d.owner[key] = file
	if len(d.order) < DedupSize {
		d.order = append(d.order, key)
	} else {
		delete(d.owner, d.order[d.next])
		d.order[d.next] = key
		d.next = (d.next + 1) % DedupSize
	}
	return true, false
}
