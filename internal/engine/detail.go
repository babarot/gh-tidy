package engine

import (
	"github.com/babarot/gh-tidy/internal/model"
)

// keepDetails is how many rows' details are held; the preview shows one,
// and moving back to a row seen a moment ago shows it at once.
const keepDetails = 16

// Detail is what the preview shows of a clone, or of its linked worktree
// at tree when tree is not empty: the commits, files and stash behind the
// row's marks. When it is not held it is read, first among the reads of
// the disk, and the screen is told when it arrives; ok is false
// meanwhile. A clone read again forgets what was held of it.
func (e *Engine) Detail(clone model.CloneKey, tree string) (model.Detail, bool) {
	k := detailKey{clone, tree}
	e.mu.Lock()
	if d, ok := e.details[k]; ok {
		e.mu.Unlock()
		return d, true
	}
	if e.detailing[k] {
		e.mu.Unlock()
		return model.Detail{}, false
	}
	var c model.Clone
	found := false
	for _, o := range e.owners {
		if r := o.rows[string(clone)]; r != nil && r.Local.Has {
			c, found = r.Local.Value, true
			break
		}
	}
	if !found {
		e.mu.Unlock()
		return model.Detail{}, false
	}
	e.detailing[k] = true
	epoch, reads := e.epoch[clone], e.reads[clone]
	// Queued after letting go of e.mu: the pool asks tasks whether they
	// are alive under its own lock, and they take e.mu.
	e.mu.Unlock()
	e.localPool.add(prioAsked, func() bool { return true }, func() {
		d := e.local.Detail(e.ctx, c, tree)
		e.mu.Lock()
		delete(e.detailing, k)
		// A change started or ended meanwhile, or the clone was read again:
		// the detail is of an older copy, read again when asked.
		if e.epoch[clone] == epoch && e.reads[clone] == reads {
			if len(e.details) >= keepDetails {
				clear(e.details)
			}
			e.details[k] = d
		}
		e.mu.Unlock()
		e.notify()
	}, func() {
		e.mu.Lock()
		delete(e.detailing, k)
		e.mu.Unlock()
	})
	return model.Detail{}, false
}

// forgetDetails drops what is held of clone's details, after it was read
// again. Called with e.mu held.
func (e *Engine) forgetDetails(clone model.CloneKey) {
	for k := range e.details {
		if k.clone == clone {
			delete(e.details, k)
		}
	}
}
