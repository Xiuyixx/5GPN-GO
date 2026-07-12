package resolver

import "sync/atomic"

// Metrics holds lock-free counters for the resolver hot path. Snapshot is
// consumed by the panel's /api/v1/metrics/dns endpoint.
type Metrics struct {
	queriesTotal     atomic.Int64
	hitsBlock        atomic.Int64
	hitsDirect       atomic.Int64
	hitsProxy        atomic.Int64
	hitsSpoof        atomic.Int64
	upstreamErrors   atomic.Int64
	upstreamFallback atomic.Int64
	refusedAXFR      atomic.Int64
}

// Snapshot is a point-in-time copy safe to serialize or compare in tests.
type Snapshot struct {
	QueriesTotal     int64 `json:"queries_total"`
	HitsBlock        int64 `json:"hits_block"`
	HitsDirect       int64 `json:"hits_direct"`
	HitsProxy        int64 `json:"hits_proxy"`
	HitsSpoof        int64 `json:"hits_spoof"`
	UpstreamErrors   int64 `json:"upstream_errors"`
	UpstreamFallback int64 `json:"upstream_fallback"`
	RefusedAXFR      int64 `json:"refused_axfr"`
}

// NewMetrics returns a zeroed counter set.
func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) IncQueries()          { m.queriesTotal.Add(1) }
func (m *Metrics) IncBlock()            { m.hitsBlock.Add(1) }
func (m *Metrics) IncDirect()           { m.hitsDirect.Add(1) }
func (m *Metrics) IncProxy()            { m.hitsProxy.Add(1) }
func (m *Metrics) IncSpoof()            { m.hitsSpoof.Add(1) }
func (m *Metrics) IncUpstreamError()    { m.upstreamErrors.Add(1) }
func (m *Metrics) IncUpstreamFallback() { m.upstreamFallback.Add(1) }
func (m *Metrics) IncRefusedAXFR()      { m.refusedAXFR.Add(1) }

// Snapshot reads all counters. Individual fields may be inconsistent with
// each other under concurrent writers (each atomic.Int64 load is
// independent) — acceptable for a dashboard gauge, not used for billing.
func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		QueriesTotal:     m.queriesTotal.Load(),
		HitsBlock:        m.hitsBlock.Load(),
		HitsDirect:       m.hitsDirect.Load(),
		HitsProxy:        m.hitsProxy.Load(),
		HitsSpoof:        m.hitsSpoof.Load(),
		UpstreamErrors:   m.upstreamErrors.Load(),
		UpstreamFallback: m.upstreamFallback.Load(),
		RefusedAXFR:      m.refusedAXFR.Load(),
	}
}

// discardMetrics returns a *Metrics for callers that don't need to read
// counters back. Each call returns a fresh sink so parallel tests never
// share writer state; the allocation is cheap (eight atomic.Int64s).
func discardMetrics() *Metrics { return NewMetrics() }
