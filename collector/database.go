package collector

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/api/rdb/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// DatabaseCollector collects metrics about all databases.
type DatabaseCollector struct {
	logger    log.Logger
	errors    *prometheus.CounterVec
	client    *scw.Client
	rdbClient *rdb.API
	timeout   time.Duration
	regions   []scw.Region

	Up         *prometheus.Desc
	CPUs       *prometheus.Desc
	Memory     *prometheus.Desc
	Connection *prometheus.Desc
	Disk       *prometheus.Desc
}

// NewDatabaseCollector returns a new DatabaseCollector.
func NewDatabaseCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration, regions []scw.Region) *DatabaseCollector {
	errors.WithLabelValues("database").Add(0)

	_ = level.Info(logger).Log("msg", "Database collector enabled")

	labels := []string{"id", "name", "region", "engine", "type"}

	labelsNode := []string{"id", "name", "node"}

	return &DatabaseCollector{
		logger:    logger,
		errors:    errors,
		client:    client,
		rdbClient: rdb.NewAPI(client),
		timeout:   timeout,
		regions:   regions,

		Up: prometheus.NewDesc(
			"scaleway_database_up",
			"If 1 the database is up and running, 0.5 in autohealing, 0 otherwise",
			labels, nil,
		),
		CPUs: prometheus.NewDesc(
			"scaleway_database_cpu_usage_percent",
			"Database's CPUs percentage usage",
			labelsNode, nil,
		),
		Memory: prometheus.NewDesc(
			"scaleway_database_memory_usage_percent",
			"Database's memory percentage usage",
			labelsNode, nil,
		),
		Connection: prometheus.NewDesc(
			"scaleway_database_total_connections",
			"Database's connection count",
			labelsNode, nil,
		),
		Disk: prometheus.NewDesc(
			"scaleway_database_disk_usage_percent",
			"Database's disk percentage usage",
			labelsNode, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *DatabaseCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.Up
	ch <- c.CPUs
	ch <- c.Memory
	ch <- c.Connection
	ch <- c.Disk
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *DatabaseCollector) Collect(ch chan<- prometheus.Metric) {

	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	var wg sync.WaitGroup
	defer wg.Wait()

	for _, region := range c.regions {

		// create a list to hold our databases
		response, err := c.rdbClient.ListInstances(&rdb.ListInstancesRequest{Region: region}, scw.WithAllPages())

		if err != nil {
			c.errors.WithLabelValues("database").Add(1)
			_ = level.Warn(c.logger).Log(
				"msg", "can't fetch the list of databases",
				"region", region,
				"err", err,
			)

			return
		}

		_ = level.Debug(c.logger).Log(
			"msg", fmt.Sprintf("found %d database instances", len(response.Instances)),
			"region", region,
		)

		for _, instance := range response.Instances {

			wg.Add(1)

			_ = level.Debug(c.logger).Log("msg", fmt.Sprintf("Fetching metrics for database instance : %s", instance.Name))

			go c.FetchMetricsForInstance(&wg, ch, instance)
		}
	}

}

func (c *DatabaseCollector) FetchMetricsForInstance(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, instance *rdb.Instance) {

	defer parentWg.Done()

	labels := []string{
		instance.ID,
		instance.Name,
		instance.Region.String(),
		instance.Engine,
		instance.NodeType,
	}

	// TODO check if it is possible to add database tag as labels
	//for _, tags := range instance.Tags {
	//	labels = append(labels, tags)
	//}

	var active float64

	switch instance.Status {
	case rdb.InstanceStatusReady:
		active = 1.0
	case rdb.InstanceStatusBackuping:
		active = 1.0
	case rdb.InstanceStatusAutohealing:
		active = 0.5
	default:
		active = 0.0
	}

	ch <- prometheus.MustNewConstMetric(
		c.Up,
		prometheus.GaugeValue,
		active,
		labels...,
	)

	metricResponse, err := c.rdbClient.GetInstanceMetrics(&rdb.GetInstanceMetricsRequest{Region: instance.Region, InstanceID: instance.ID})

	if err != nil {
		c.errors.WithLabelValues("database").Add(1)
		_ = level.Warn(c.logger).Log(
			"msg", "can't fetch the metric for the instance",
			"err", err,
			"region", instance.Region,
			"instanceId", instance.ID,
			"instanceName", instance.Name,
		)

		return
	}

	for _, timeseries := range metricResponse.Timeseries {

		labelsNode := []string{
			instance.ID,
			instance.Name,
			timeseries.Metadata["node"],
		}

		var series *prometheus.Desc

		switch timeseries.Name {
		case "cpu_usage_percent":
			series = c.CPUs
		case "mem_usage_percent":
			series = c.Memory
		case "total_connections":
			series = c.Connection
		case "disk_usage_percent":
			series = c.Disk
		default:
			_ = level.Debug(c.logger).Log(
				"msg", "unmapped scaleway metric",
				"err", err,
				"region", instance.Region,
				"instanceId", instance.ID,
				"instanceName", instance.Name,
				"scwMetric", timeseries.Name,
			)
			continue
		}

		if len(timeseries.Points) == 0 {
			c.errors.WithLabelValues("database").Add(1)
			_ = level.Warn(c.logger).Log(
				"msg", "no data were returned for the metric",
				"instanceName", instance.Name,
				"instanceId", instance.ID,
				"metric", timeseries.Name,
				"region", instance.Region,
				"err", err,
			)

			continue
		}

		sort.Slice(timeseries.Points, func(i, j int) bool {
			return timeseries.Points[i].Timestamp.Before(timeseries.Points[j].Timestamp)
		})

		value := float64(timeseries.Points[len(timeseries.Points)-1].Value)

		ch <- prometheus.MustNewConstMetric(series, prometheus.GaugeValue, value, labelsNode...)
	}
}
