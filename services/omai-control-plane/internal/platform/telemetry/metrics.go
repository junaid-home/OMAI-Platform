package telemetry

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Metrics struct {
	Requests      atomic.Uint64
	Errors        atomic.Uint64
	RateLimited   atomic.Uint64
	ActiveStreams atomic.Int64
	ActiveRuns    atomic.Int64
	VoiceActive   atomic.Int64
	VoiceStarted  atomic.Uint64
	VoiceErrors   atomic.Uint64
	StartedAt     time.Time
}

func New() *Metrics {
	return &Metrics{StartedAt: time.Now()}
}

func (m *Metrics) Handler(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(response,
		"# TYPE omai_requests_total counter\nomai_requests_total %d\n"+
			"# TYPE omai_errors_total counter\nomai_errors_total %d\n"+
			"# TYPE omai_rate_limited_total counter\nomai_rate_limited_total %d\n"+
			"# TYPE omai_active_streams gauge\nomai_active_streams %d\n"+
			"# TYPE omai_active_runs gauge\nomai_active_runs %d\n"+
			"# TYPE omai_voice_sessions_active gauge\nomai_voice_sessions_active %d\n"+
			"# TYPE omai_voice_sessions_started_total counter\nomai_voice_sessions_started_total %d\n"+
			"# TYPE omai_voice_errors_total counter\nomai_voice_errors_total %d\n"+
			"# TYPE omai_uptime_seconds gauge\nomai_uptime_seconds %.0f\n",
		m.Requests.Load(), m.Errors.Load(), m.RateLimited.Load(),
		m.ActiveStreams.Load(), m.ActiveRuns.Load(), m.VoiceActive.Load(), m.VoiceStarted.Load(), m.VoiceErrors.Load(), time.Since(m.StartedAt).Seconds(),
	)
}
