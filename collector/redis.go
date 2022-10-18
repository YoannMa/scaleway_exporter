package collector

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// RedisCollector collects metrics about all redis nodes.
type RedisCollector struct {
	logger  log.Logger
	errors  *prometheus.CounterVec
	client  *scw.Client
	timeout time.Duration
	zones   []scw.Zone

	CpuUsagePercent      *prometheus.Desc
	MemUsagePercent      *prometheus.Desc
	DbMemoryUsagePercent *prometheus.Desc
}

// NewRedisCollector returns a new RedisCollector.
func NewRedisCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration, zones []scw.Zone) *RedisCollector {
	errors.WithLabelValues("redis").Add(0)

	labels := []string{"id", "name", "node"}
	return &RedisCollector{
		logger:  logger,
		errors:  errors,
		client:  client,
		timeout: timeout,
		zones:   zones,

		CpuUsagePercent: prometheus.NewDesc(
			"scaleway_redis_cpu_usage_percent",
			"The redis node CPU usage percentage",
			labels, nil,
		),
		MemUsagePercent: prometheus.NewDesc(
			"scaleway_redis_memory_usage_percent",
			"The redis node memory usage percentage",
			labels, nil,
		),
		DbMemoryUsagePercent: prometheus.NewDesc(
			"scaleway_redis_db_memory_usage_percent",
			"The redis node database memory usage percentage",
			labels, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *RedisCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.CpuUsagePercent
	ch <- c.MemUsagePercent
	ch <- c.DbMemoryUsagePercent
}

type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Clusters struct {
	Cluster []*Cluster `json:"clusters"`
}

// InstanceMetrics: instance metrics
type RedisMetrics struct {
	// Timeseries: time series of metrics of a given instance
	Timeseries []*scw.TimeSeries `json:"timeseries"`
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *RedisCollector) Collect(ch chan<- prometheus.Metric) {

	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var wg sync.WaitGroup
	defer wg.Wait()

	for _, zone := range c.zones {

		scwReq := &scw.ScalewayRequest{
			Method:  "GET",
			Path:    "/redis/v1/zones/" + fmt.Sprint(zone) + "/clusters",
			Headers: http.Header{},
		}

		var response Clusters

		err := c.client.Do(scwReq, &response)

		if err != nil {
			c.errors.WithLabelValues("clusters").Add(1)
			_ = level.Warn(c.logger).Log("msg", "can't fetch clusters", "err", err)

			return
		}

		var wg sync.WaitGroup
		defer wg.Wait()

		for _, cluster := range response.Cluster {

			wg.Add(1)

			_ = level.Debug(c.logger).Log("msg", fmt.Sprintf("Fetching metrics for cluster : %s", cluster.ID))

			go c.FetchRedisMetrics(&wg, ch, zone, cluster)
		}

	}
}

func (c *RedisCollector) FetchRedisMetrics(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, zone scw.Zone, cluster *Cluster) {

	defer parentWg.Done()

	scwReq := &scw.ScalewayRequest{
		Method: "GET",
		Path:   "/redis/v1/zones/" + fmt.Sprint(zone) + "/clusters/" + fmt.Sprint(cluster.ID) + "/metrics",
	}

	var metricResponse RedisMetrics

	err := c.client.Do(scwReq, &metricResponse)

	if err != nil {
		c.errors.WithLabelValues("redis").Add(1)
		_ = level.Warn(c.logger).Log(
			"msg", "can't fetch the metric for the redis cluster",
			"err", err,
			"clusterId", cluster.ID,
			"clusterName", cluster.Name,
		)

		return
	}

	for _, timeseries := range metricResponse.Timeseries {

		labels := []string{
			cluster.ID,
			cluster.Name,
			timeseries.Metadata["node"],
		}

		var series *prometheus.Desc

		switch timeseries.Name {
		case "cpu_usage_percent":
			series = c.CpuUsagePercent
		case "mem_usage_percent":
			series = c.MemUsagePercent
		case "db_memory_usage_percent":
			series = c.DbMemoryUsagePercent
		default:
			_ = level.Debug(c.logger).Log(
				"msg", "unmapped scaleway metric",
				"err", err,
				"clusterId", cluster.ID,
				"clusterName", cluster.Name,
				"scwMetric", timeseries.Name,
			)
			continue
		}

		if len(timeseries.Points) == 0 {
			c.errors.WithLabelValues("redis").Add(1)
			_ = level.Warn(c.logger).Log(
				"msg", "no data were returned for the metric",
				"err", err,
				"clusterId", cluster.ID,
				"clusterName", cluster.Name,
				"metric", series,
			)

			continue
		}

		sort.Slice(timeseries.Points, func(i, j int) bool {
			return timeseries.Points[i].Timestamp.Before(timeseries.Points[j].Timestamp)
		})

		value := float64(timeseries.Points[len(timeseries.Points)-1].Value)

		ch <- prometheus.MustNewConstMetric(series, prometheus.GaugeValue, value, labels...)
	}
}
