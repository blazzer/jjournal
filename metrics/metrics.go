// Package metrics writes a small Prometheus text exposition.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Buckets are the fixed histogram bounds, in seconds.
var Buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type kind int

const (
	kindCounter kind = iota
	kindGauge
	kindHistogram
)

type series struct {
	name   string
	help   string
	kind   kind
	labels string
	value  float64
	sum    float64
	count  uint64
	bucket []uint64
}

// Registry collects metrics.
type Registry struct {
	mu sync.Mutex
	m  map[string]*series
}

func New() *Registry { return &Registry{m: map[string]*series{}} }

func key(name, labels string) string { return name + "\x00" + labels }

func labelsOf(pairs []string) string {
	if len(pairs)%2 != 0 {
		pairs = append(pairs, "")
	}
	var b strings.Builder
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", pairs[i], pairs[i+1])
	}
	return b.String()
}

func (r *Registry) add(name, help string, k kind, labels string) *series {
	id := key(name, labels)
	s := r.m[id]
	if s == nil {
		s = &series{name: name, help: help, kind: k, labels: labels}
		if k == kindHistogram {
			s.bucket = make([]uint64, len(Buckets))
		}
		r.m[id] = s
	}
	return s
}

// AddCounter adds n to a counter.
func (r *Registry) AddCounter(name, help string, n float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(name, help, kindCounter, labelsOf(labels)).value += n
}

// SetGauge sets a gauge.
func (r *Registry) SetGauge(name, help string, v float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(name, help, kindGauge, labelsOf(labels)).value = v
}

// Observe records a histogram observation.
func (r *Registry) Observe(name, help string, v float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.add(name, help, kindHistogram, labelsOf(labels))
	s.sum += v
	s.count++
	for i, b := range Buckets {
		if v <= b {
			s.bucket[i]++
		}
	}
}

// WriteTo writes the exposition.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keys []string
	for k := range r.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var n int64
	seenHelp := map[string]bool{}
	for _, k := range keys {
		s := r.m[k]
		if !seenHelp[s.name] {
			seenHelp[s.name] = true
			c, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", s.name, s.help, s.name, typeName(s.kind))
			n += int64(c)
			if err != nil {
				return n, err
			}
		}
		wrote, err := writeSeries(w, s)
		n += wrote
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func typeName(k kind) string {
	switch k {
	case kindGauge:
		return "gauge"
	case kindHistogram:
		return "histogram"
	default:
		return "counter"
	}
}

func writeSeries(w io.Writer, s *series) (int64, error) {
	label := func(extra string) string {
		switch {
		case s.labels == "" && extra == "":
			return ""
		case s.labels == "":
			return "{" + extra + "}"
		case extra == "":
			return "{" + s.labels + "}"
		default:
			return "{" + s.labels + "," + extra + "}"
		}
	}
	if s.kind != kindHistogram {
		c, err := fmt.Fprintf(w, "%s%s %g\n", s.name, label(""), s.value)
		return int64(c), err
	}
	var n int64
	for i, b := range Buckets {
		c, err := fmt.Fprintf(w, "%s_bucket%s %d\n", s.name, label(fmt.Sprintf("le=%q", trim(b))), s.bucket[i])
		n += int64(c)
		if err != nil {
			return n, err
		}
	}
	c, err := fmt.Fprintf(w, "%s_bucket%s %d\n", s.name, label(`le="+Inf"`), s.count)
	n += int64(c)
	if err != nil {
		return n, err
	}
	c, err = fmt.Fprintf(w, "%s_sum%s %g\n%s_count%s %d\n", s.name, label(""), s.sum, s.name, label(""), s.count)
	n += int64(c)
	return n, err
}

func trim(b float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%g", b), "0"), ".")
}
