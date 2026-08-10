package updater

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arnaudcharles/doupro/internal/events"
	"github.com/arnaudcharles/doupro/internal/metrics"
)

// Operation describes the single destructive operation currently allowed for
// a container. Container names are used as keys because Docker IDs change as
// part of a successful recreation.
type Operation struct {
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
}

// OperationInProgressError is returned when an update or rollback is already
// mutating the same container. API callers can reliably map it to HTTP 409.
type OperationInProgressError struct {
	Container string
	Current   Operation
}

// MaintenanceInProgressError tells background schedulers to pause without
// consuming their durable job. It is distinct from ordinary per-container
// contention, which is a genuine rejected execution.
type MaintenanceInProgressError struct{}

func (*MaintenanceInProgressError) Error() string {
	return "DoUpRo self-update maintenance is in progress"
}

func (e *OperationInProgressError) Error() string {
	return fmt.Sprintf("%s already has a %s operation in progress", e.Container, e.Current.Kind)
}

type operationGuard struct {
	mu          sync.RWMutex
	active      map[string]Operation
	maintenance bool
}

func newOperationGuard() *operationGuard {
	return &operationGuard{active: make(map[string]Operation)}
}

func (g *operationGuard) begin(name, kind string) (Operation, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maintenance {
		return Operation{Kind: "self-update", StartedAt: time.Now().UTC()}, false
	}
	if current, ok := g.active[name]; ok {
		return current, false
	}
	g.active[name] = Operation{Kind: kind, StartedAt: time.Now().UTC()}
	metrics.ContainerOperationsActive.WithLabelValues(kind).Inc()
	return Operation{}, true
}

func (g *operationGuard) beginMaintenance() ([]string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maintenance || len(g.active) != 0 {
		active := make([]string, 0, len(g.active))
		for name, op := range g.active {
			active = append(active, name+":"+op.Kind)
		}
		return active, false
	}
	g.maintenance = true
	return nil, true
}

func (g *operationGuard) endMaintenance() {
	g.mu.Lock()
	g.maintenance = false
	g.mu.Unlock()
}

func (g *operationGuard) end(name, kind string) {
	g.mu.Lock()
	delete(g.active, name)
	g.mu.Unlock()
	metrics.ContainerOperationsActive.WithLabelValues(kind).Dec()
}

func (g *operationGuard) get(name string) (Operation, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	op, ok := g.active[name]
	return op, ok
}

func (g *operationGuard) maintenanceActive() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.maintenance
}

func (u *Updater) beginOperation(name, kind string, actor events.Actor, actorID string) (func(), error) {
	current, ok := u.operations.begin(name, kind)
	if !ok {
		metrics.ContainerOperationRejectionsTotal.WithLabelValues(kind).Inc()
		u.logger.Emit(context.Background(), events.Event{
			Level: events.LevelWarn, Type: "container.operation_rejected", Container: name,
			Actor: actor, ActorID: actorID,
			Message:  fmt.Sprintf("rejected %s for %s: %s already in progress", kind, name, current.Kind),
			Metadata: map[string]any{"requested_operation": kind, "active_operation": current.Kind},
		})
		if current.Kind == "self-update" {
			return nil, &MaintenanceInProgressError{}
		}
		return nil, &OperationInProgressError{Container: name, Current: current}
	}
	return func() { u.operations.end(name, kind) }, nil
}

// Operation returns the active destructive operation for a container name.
func (u *Updater) Operation(name string) (Operation, bool) {
	return u.operations.get(name)
}

// MaintenanceActive lets scheduler ticks freeze without consuming jobs.
func (u *Updater) MaintenanceActive() bool { return u.operations.maintenanceActive() }

// IsMaintenanceError reports the race-safe refusal returned when maintenance
// began after a scheduler tick had already started resolving targets.
func IsMaintenanceError(err error) bool {
	var target *MaintenanceInProgressError
	return errors.As(err, &target)
}
