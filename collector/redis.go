package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/api/redis/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// RedisCollector collects metrics about all redis nodes.
type RedisCollector struct {
	logger      log.Logger
	errors      *prometheus.CounterVec
	client      *scw.Client
	redisClient *redis.API
	timeout     time.Duration
	zones       []scw.Zone

	CPUUsagePercent      *prometheus.Desc
	MemUsagePercent      *prometheus.Desc
	DBMemoryUsagePercent *prometheus.Desc
}

// NewRedisCollector returns a new RedisCollector.
func NewRedisCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration, zones []scw.Zone) *RedisCollector {
	errors.WithLabelValues("redis").Add(0)

	_ = level.Info(logger).Log("msg", "Redis collector enabled")

	labels := []string{"id", "name", "node"}

	return &RedisCollector{
		logger:      logger,
		errors:      errors,
		client:      client,
		redisClient: redis.NewAPI(client),
		timeout:     timeout,
		zones:       zones,

		CPUUsagePercent: prometheus.NewDesc(
			"scaleway_redis_cpu_usage_percent",
			"The redis node CPU usage percentage",
			labels, nil,
		),
		MemUsagePercent: prometheus.NewDesc(
			"scaleway_redis_memory_usage_percent",
			"The redis node memory usage percentage",
			labels, nil,
		),
		DBMemoryUsagePercent: prometheus.NewDesc(
			"scaleway_redis_db_memory_usage_percent",
			"The redis node database memory usage percentage",
			labels, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *RedisCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.CPUUsagePercent
	ch <- c.MemUsagePercent
	ch <- c.DBMemoryUsagePercent
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *RedisCollector) Collect(ch chan<- prometheus.Metric) {
	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	for _, zone := range c.zones {
		clusterList, err := c.redisClient.ListClusters(&redis.ListClustersRequest{Zone: zone})

		if err != nil {
			var responseError *scw.ResponseError

			switch {
			case errors.As(err, &responseError) && responseError.StatusCode == http.StatusNotImplemented:
				_ = level.Debug(c.logger).Log("msg", "Loadbalancer is not supported in this zone", "zone", zone)
				return
			default:
				c.errors.WithLabelValues("clusters").Add(1)
				_ = level.Warn(c.logger).Log("msg", "can't fetch the list of clusters", "err", err, "zone", zone)

				return
			}
		}

		var wg sync.WaitGroup
		defer wg.Wait()

		for _, cluster := range clusterList.Clusters {
			wg.Add(1)

			_ = level.Debug(c.logger).Log("msg", fmt.Sprintf("Fetching metrics for cluster : %s", cluster.ID), "zone", zone)

			go c.FetchRedisMetrics(&wg, ch, zone, cluster)
		}
	}
}

func (c *RedisCollector) FetchRedisMetrics(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, zone scw.Zone, cluster *redis.Cluster) {
	defer parentWg.Done()

	metricResponse, err := c.redisClient.GetClusterMetrics(&redis.GetClusterMetricsRequest{
		Zone:      zone,
		ClusterID: cluster.ID,
	})

	if err != nil {
		c.errors.WithLabelValues("redis").Add(1)
		_ = level.Warn(c.logger).Log(
			"msg", "can't fetch the metric for the redis cluster",
			"clusterName", cluster.Name,
			"clusterId", cluster.ID,
			"zone", zone,
			"err", err,
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
			series = c.CPUUsagePercent
		case "mem_usage_percent":
			series = c.MemUsagePercent
		case "db_memory_usage_percent":
			series = c.DBMemoryUsagePercent
		default:
			_ = level.Debug(c.logger).Log(
				"msg", "unmapped scaleway metric",
				"scwMetric", timeseries.Name,
				"clusterName", cluster.Name,
				"clusterId", cluster.ID,
				"zone", zone,
				"err", err,
			)
			continue
		}

		if len(timeseries.Points) == 0 {
			c.errors.WithLabelValues("redis").Add(1)
			_ = level.Warn(c.logger).Log(
				"msg", "no data were returned for the metric",
				"metric", timeseries.Name,
				"clusterName", cluster.Name,
				"clusterId", cluster.ID,
				"zone", zone,
				"err", err,
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
