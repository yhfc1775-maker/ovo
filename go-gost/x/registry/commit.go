package registry

import (
	"errors"
	"io"

	reg "github.com/go-gost/core/registry"
)

// CommitNamed publishes a prepared object only after its configuration commits.
// The caller holds config.LockMutation, including validation and preparation.
// Swapping a native registry entry cannot fail after persistence succeeds.
func CommitNamed[T any](r reg.Registry[T], name string, value T, remove bool, commit func() error) error {
	target, ok := r.(interface{ commitValue(string, T, bool) })
	if !ok {
		return errors.New("registry does not support atomic publication")
	}
	if err := commit(); err != nil {
		return err
	}
	target.commitValue(name, value, remove)
	return nil
}

func (r *registry[T]) commitValue(name string, value T, remove bool) {
	var old any
	var loaded bool
	if remove {
		old, loaded = r.m.LoadAndDelete(name)
	} else {
		old, loaded = r.m.Swap(name, value)
	}
	if loaded {
		if retirer, ok := old.(interface{ Retire() }); ok {
			retirer.Retire()
		} else if closer, ok := old.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}
