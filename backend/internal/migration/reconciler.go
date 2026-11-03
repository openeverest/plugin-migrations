package migration

import (
	"context"
	"log"
	"time"
)

// Reconcile performs one pass: refresh every worker's cached status and start
// migrations whose worker is up but idle, so a migration starts even when no
// browser is open. It needs no user token: it only talks to workers.
func (s *Service) Reconcile(ctx context.Context) {
	records, err := s.store.List(ctx)
	if err != nil {
		log.Printf("reconcile: list migrations: %v", err)
		return
	}
	live := make(map[string]struct{}, len(records))
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		live[record.ID] = struct{}{}
		observed := s.observe(ctx, record)
		if record.StartedAt == nil && observed.status.Idle {
			if started, ok := s.start(ctx, record); ok {
				record = started
				observed = s.observe(ctx, record)
			}
		}
		s.storeObservation(record.ID, observed)
	}

	s.cacheMu.Lock()
	for id := range s.cache {
		if _, exists := live[id]; !exists {
			delete(s.cache, id)
		}
	}
	s.cacheMu.Unlock()
}

func (s *Service) start(ctx context.Context, record Record) (Record, bool) {
	registration, ok := s.registry.Lookup(record.Engine)
	if !ok {
		return record, false
	}
	callCtx, cancel := context.WithTimeout(ctx, workerCallTimeout)
	defer cancel()
	controller := registration.Engine.Controller(s.workloads.BaseURL(record.ID, record.WorkerPort))
	if err := controller.Start(callCtx, record.Options); err != nil {
		log.Printf("migration %s: start: %v", record.ID, err)
		return record, false
	}
	now := s.now().UTC()
	record.StartedAt = &now
	updated, err := s.store.Update(ctx, record)
	if err != nil {
		// The engine treats a repeated start as success, so the next pass retries safely.
		log.Printf("migration %s: record start: %v", record.ID, err)
		return record, false
	}
	log.Printf("migration %s: started", record.ID)
	return updated, true
}

// RunReconciler reconciles immediately and then every interval until ctx ends.
func (s *Service) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
