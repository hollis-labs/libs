package plugins

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pluginhost "github.com/hollis-labs/plugin-host"
)

// Inventory contains only app-reviewed exact Specs. Discovery, secrets, grants,
// restart policy and business calls stay in the application.
// Specs are immutable after construction: callers must not mutate nested init
// fields or referenced slices/maps. The adapter never changes reviewed Specs.
type Inventory struct {
	mu        sync.Mutex
	specs     []pluginhost.Spec
	processes []*pluginhost.Process
	running   bool
}

func NewInventory(specs []pluginhost.Spec) *Inventory {
	return &Inventory{specs: append([]pluginhost.Spec(nil), specs...)}
}

// Start is all-or-nothing admission before listening. It serializes lifecycle
// changes, preserves each shared driver's captured process identity and rolls
// back started processes on failure. A canceled boot context does not own life.
func (i *Inventory) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.running {
		return fmt.Errorf("plugins: inventory already running")
	}
	seen := map[string]bool{}
	for _, spec := range i.specs {
		if spec.ID == "" || seen[spec.ID] {
			return fmt.Errorf("plugins: missing/duplicate inventory identity")
		}
		seen[spec.ID] = true
	}
	for _, spec := range i.specs {
		p, err := pluginhost.Start(ctx, spec)
		if err != nil {
			for _, old := range i.processes {
				_ = old.Stop(context.Background())
			}
			i.processes = nil
			return err
		}
		if p.Info().ID != spec.ID {
			_ = p.Stop(context.Background())
			for _, old := range i.processes {
				_ = old.Stop(context.Background())
			}
			i.processes = nil
			return fmt.Errorf("plugins: handshake identity mismatch")
		}
		i.processes = append(i.processes, p)
	}
	i.running = true
	return nil
}
func (i *Inventory) Stop(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	var err error
	for _, p := range i.processes {
		err = errors.Join(err, p.Stop(ctx))
	}
	i.processes = nil
	i.running = false
	return err
}
