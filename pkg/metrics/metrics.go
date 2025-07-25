/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/version"
	log "github.com/sirupsen/logrus"

	cfg "sigs.k8s.io/external-dns/pkg/apis/externaldns"
)

const (
	Namespace = "external_dns"
)

var (
	RegisterMetric = NewMetricsRegister()
)

func init() {
	RegisterMetric.MustRegister(NewGaugeFuncMetric(prometheus.GaugeOpts{
		Namespace: Namespace,
		Name:      "build_info",
		Help: fmt.Sprintf(
			"A metric with a constant '1' value labeled with 'version' and 'revision' of %s and the 'go_version', 'os' and the 'arch' used the build.",
			Namespace,
		),
		ConstLabels: prometheus.Labels{
			"version":    cfg.Version,
			"revision":   version.GetRevision(),
			"go_version": version.GoVersion,
			"os":         version.GoOS,
			"arch":       version.GoArch,
		},
	}))
}

func NewMetricsRegister() *MetricRegistry {
	reg := prometheus.WrapRegistererWith(
		prometheus.Labels{},
		prometheus.DefaultRegisterer)
	return &MetricRegistry{
		Registerer: reg,
		Metrics:    []*Metric{},
		mName:      make(map[string]bool),
	}
}

// MustRegister registers a metric if it hasn't been registered yet.
//
// Usage: MustRegister(...)
// Example:
//
//	func init() {
//	     metrics.RegisterMetric.MustRegister(errorsTotal)
//	}
func (m *MetricRegistry) MustRegister(cs IMetric) {
	switch v := cs.(type) {
	case CounterMetric, GaugeMetric, CounterVecMetric, GaugeVecMetric, GaugeFuncMetric:
		if _, exists := m.mName[cs.Get().FQDN]; exists {
			return
		} else {
			m.mName[cs.Get().FQDN] = true
		}
		m.Metrics = append(m.Metrics, cs.Get())
		switch metric := v.(type) {
		case CounterMetric:
			m.Registerer.MustRegister(metric.Counter)
		case GaugeMetric:
			m.Registerer.MustRegister(metric.Gauge)
		case GaugeVecMetric:
			m.Registerer.MustRegister(metric.Gauge)
		case CounterVecMetric:
			m.Registerer.MustRegister(metric.CounterVec)
		case GaugeFuncMetric:
			m.Registerer.MustRegister(metric.GaugeFunc)
		}
		log.Debugf("Register metric: %s", cs.Get().FQDN)
	default:
		log.Warnf("Unsupported metric type: %T", v)
		return
	}
}
