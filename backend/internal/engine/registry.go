package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Registration binds a compiled-in engine to its configured runner image.
type Registration struct {
	Engine Engine
	Runner RunnerConfig
}

// Registry is built once at startup and is safe for concurrent lookups.
type Registry struct {
	registrations map[string]Registration
}

// NewRegistry accepts only enabled registrations from trusted configuration.
func NewRegistry(registrations ...Registration) (*Registry, error) {
	if len(registrations) == 0 {
		return nil, errors.New("at least one enabled engine is required")
	}
	registry := &Registry{registrations: make(map[string]Registration, len(registrations))}
	for _, registration := range registrations {
		if registration.Engine == nil {
			return nil, errors.New("registered engine is required")
		}
		id := registration.Engine.ID()
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("engine ID must be nonempty without surrounding whitespace")
		}
		if _, exists := registry.registrations[id]; exists {
			return nil, fmt.Errorf("engine %q is registered twice", id)
		}
		if err := registration.Runner.Validate(); err != nil {
			return nil, fmt.Errorf("engine %q: %w", id, err)
		}
		registry.registrations[id] = registration
	}
	return registry, nil
}

// Descriptors returns enabled engines sorted by ID.
func (r *Registry) Descriptors() []Descriptor {
	descriptors := make([]Descriptor, 0, len(r.registrations))
	for _, registration := range r.registrations {
		descriptors = append(descriptors, registration.Engine.Describe())
	}
	slices.SortFunc(descriptors, func(a, b Descriptor) int { return strings.Compare(a.ID, b.ID) })
	return descriptors
}

// Lookup returns an enabled engine without checking endpoint compatibility.
func (r *Registry) Lookup(id string) (Registration, bool) {
	registration, ok := r.registrations[id]
	return registration, ok
}

// Resolve selects exactly the requested engine and checks it supports the
// endpoints. It never infers an engine.
func (r *Registry) Resolve(id string, source, target EndpointInfo) (Registration, error) {
	if strings.TrimSpace(id) == "" {
		return Registration{}, &Error{Kind: KindInvalidRequest, Message: "engine is required"}
	}
	registration, ok := r.registrations[id]
	if !ok {
		return Registration{}, &Error{Kind: KindInvalidRequest, Message: "engine is not available"}
	}
	if !registration.Engine.Supports(source, target) {
		return Registration{}, &Error{Kind: KindInvalidRequest, Message: "engine does not support the selected source and target database types"}
	}
	return registration, nil
}
