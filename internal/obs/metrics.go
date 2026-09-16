package obs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the service's Prometheus registry and the instruments on it.
type Metrics struct {
	registry  *prometheus.Registry
	requests  *prometheus.HistogramVec
	raceLost  prometheus.Counter
	sweeps    *prometheus.CounterVec
	reclaimed prometheus.Counter
}

// NewMetrics registers the service instruments alongside the Go runtime,
// process and connection pool collectors.
func NewMetrics(poolStat func() *pgxpool.Stat) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		// Status is a label rather than a separate counter, because a 503 that
		// waited out the lock timeout would otherwise drag every latency up.
		requests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "cinebook_http_request_duration_seconds",
			Help:    "HTTP request latency by matched route and response status.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"route", "status"}),
		raceLost: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cinebook_seat_race_lost_total",
			Help: "Double claims caught by the unique index. Above zero means the advisory lock failed to serialize.",
		}),
		sweeps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cinebook_sweeper_runs_total",
			Help: "Sweeper runs by result.",
		}, []string{"result"}),
		reclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "cinebook_sweeper_reclaimed_seats_total",
			Help: "Lapsed seat claims the sweeper released.",
		}),
	}
	m.sweeps.WithLabelValues("ok")
	m.sweeps.WithLabelValues("error")

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.raceLost, m.sweeps, m.reclaimed,
		poolCollector{stat: poolStat},
	)
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveRequest records one HTTP request against its matched route.
func (m *Metrics) ObserveRequest(route string, status int, elapsed time.Duration) {
	m.requests.WithLabelValues(route, strconv.Itoa(status)).Observe(elapsed.Seconds())
}

// SeatRaceLost counts a double claim that the unique index caught.
func (m *Metrics) SeatRaceLost() { m.raceLost.Inc() }

// ObserveSweep records one sweeper run.
func (m *Metrics) ObserveSweep(reclaimed int64, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.sweeps.WithLabelValues(result).Inc()
	m.reclaimed.Add(float64(reclaimed))
}

// Section 4.6: a hot showtime queues transactions that each hold a connection,
// so acquired against max, and acquires that found the pool empty, are how
// pool exhaustion shows up before it stalls other showtimes.
type poolCollector struct {
	stat func() *pgxpool.Stat
}

var (
	poolMaxConns         = prometheus.NewDesc("cinebook_db_pool_max_conns", "Connection ceiling of the pool.", nil, nil)
	poolTotalConns       = prometheus.NewDesc("cinebook_db_pool_total_conns", "Open connections, idle or in use.", nil, nil)
	poolAcquiredConns    = prometheus.NewDesc("cinebook_db_pool_acquired_conns", "Connections currently checked out.", nil, nil)
	poolAcquires         = prometheus.NewDesc("cinebook_db_pool_acquires_total", "Successful connection acquires.", nil, nil)
	poolEmptyAcquires    = prometheus.NewDesc("cinebook_db_pool_empty_acquires_total", "Acquires that found the pool empty and had to wait.", nil, nil)
	poolEmptyAcquireWait = prometheus.NewDesc("cinebook_db_pool_empty_acquire_wait_seconds_total", "Time spent waiting in acquires that found the pool empty.", nil, nil)
	poolCanceledAcquires = prometheus.NewDesc("cinebook_db_pool_canceled_acquires_total", "Acquires abandoned because their context ended first.", nil, nil)
)

func (c poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		poolMaxConns, poolTotalConns, poolAcquiredConns,
		poolAcquires, poolEmptyAcquires, poolEmptyAcquireWait, poolCanceledAcquires,
	} {
		ch <- d
	}
}

func (c poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	ch <- prometheus.MustNewConstMetric(poolMaxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolTotalConns, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquiredConns, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquires, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolEmptyAcquires, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolEmptyAcquireWait, prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
	ch <- prometheus.MustNewConstMetric(poolCanceledAcquires, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
}
