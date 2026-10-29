package node

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/BlackDark/csi-driver-mergerfs/internal/metrics"
)

// Pool states in csi_mergerfs_pools.
const (
	StateStarting = "starting"
	StateReady    = "ready"
	StateLost     = "lost"
)

var poolStates = []string{StateStarting, StateReady, StateLost}

var healResults = []string{HealEvicted, HealSkipped, HealBlocked}

// Metrics are the node plugin's event counters.
type Metrics struct {
	lost      prometheus.Counter
	adoptions *prometheus.CounterVec
	heals     *prometheus.CounterVec
}

// NewMetrics registers the counters on reg; a nil reg leaves them unregistered.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		lost: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "mount_lost_total", Help: "Mount Pods that stopped without the node plugin asking.",
		}),
		adoptions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "adoptions_total", Help: "Pools found by a node plugin restart, by result.",
		}, []string{"result"}),
		heals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metrics.Namespace, Name: "workload_heals_total",
			Help: "Consumer pods handled by healing after a lost Mount Pod, by result.",
		}, []string{"result"}),
	}
	for _, r := range healResults {
		m.heals.WithLabelValues(r)
	}
	for _, r := range []string{AdoptAdopted, AdoptCleaned} {
		m.adoptions.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(m.lost, m.adoptions, m.heals)
	}
	return m
}

var (
	descPools = prometheus.NewDesc(metrics.Namespace+"_pools",
		"Pools on this node by state.", []string{"state"}, nil)
	descHealthy = prometheus.NewDesc(metrics.Namespace+"_mount_healthy",
		"1 when the pool's Mount Pod serves it, 0 while starting or after it was lost.", []string{"id", "volume_id"}, nil)
)

type poolSample struct {
	id, volumeID, state string
}

func (p *pool) sample() poolSample {
	out := poolSample{id: p.id, volumeID: p.volumeID, state: StateReady}
	switch {
	case p.isLost():
		out.state = StateLost
	case !p.ready.Load():
		out.state = StateStarting
	}
	return out
}

// collectPools turns samples into series; pure so tests need no Server.
func collectPools(samples []poolSample, ch chan<- prometheus.Metric) {
	counts := map[string]int{}
	for _, s := range samples {
		counts[s.state]++
		h := 0.0
		if s.state == StateReady {
			h = 1
		}
		ch <- prometheus.MustNewConstMetric(descHealthy, prometheus.GaugeValue, h, s.id, s.volumeID)
	}
	for _, st := range poolStates {
		ch <- prometheus.MustNewConstMetric(descPools, prometheus.GaugeValue, float64(counts[st]), st)
	}
}

// Collector reports the pools at scrape time.
func (s *Server) Collector() prometheus.Collector { return poolCollector{s} }

type poolCollector struct{ s *Server }

func (c poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descPools
	ch <- descHealthy
}

func (c poolCollector) Collect(ch chan<- prometheus.Metric) {
	c.s.mu.Lock()
	samples := make([]poolSample, 0, len(c.s.pools))
	for _, p := range c.s.pools {
		if !p.releasing.Load() || p.isLost() {
			samples = append(samples, p.sample())
		}
	}
	c.s.mu.Unlock()
	collectPools(samples, ch)
}
