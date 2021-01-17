package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/api/lb/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// LoadBalancerCollector collects metrics about all loadbalancers.
type LoadBalancerCollector struct {
	logger   log.Logger
	errors   *prometheus.CounterVec
	client   *scw.Client
	lbClient *lb.API
	timeout  time.Duration

	Up              *prometheus.Desc
	NetworkReceive  *prometheus.Desc
	NetworkTransmit *prometheus.Desc
	Connection      *prometheus.Desc
	NewConnection   *prometheus.Desc
}

// NewLoadBalancerCollector returns a new LoadBalancerCollector.
func NewLoadBalancerCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration) *LoadBalancerCollector {
	errors.WithLabelValues("loadbalancer").Add(0)

	labels := []string{"id", "name", "region", "type"}
	return &LoadBalancerCollector{
		logger:   logger,
		errors:   errors,
		client:   client,
		lbClient: lb.NewAPI(client),
		timeout:  timeout,

		Up: prometheus.NewDesc(
			"scaleway_loadbalancer_up",
			"If 1 the loadbalancer is up and running, 0.5 when migrating, 0 otherwise",
			labels, nil,
		),
		NetworkReceive: prometheus.NewDesc(
			"scaleway_loadbalancer_network_receive_bits_sec",
			"LoadBalancer's ", // TODO
			labels, nil,
		),
		NetworkTransmit: prometheus.NewDesc(
			"scaleway_loadbalancer_network_transmit_bits_sec",
			"LoadBalancer's ", // TODO
			labels, nil,
		),
		Connection: prometheus.NewDesc(
			"scaleway_loadbalancer_total_connections",
			"LoadBalancer's ", // TODO
			labels, nil,
		),
		NewConnection: prometheus.NewDesc(
			"scaleway_loadbalancer_new_connection_rate_sec",
			"LoadBalancer's ", // TODO
			labels, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *LoadBalancerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.Up
	ch <- c.NetworkReceive
	ch <- c.NetworkTransmit
	ch <- c.Connection
	ch <- c.NewConnection
}

// InstanceMetrics: instance metrics
type LbMetrics struct {
	// Timeseries: time series of metrics of a given instance
	Timeseries []*scw.TimeSeries `json:"timeseries"`
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *LoadBalancerCollector) Collect(ch chan<- prometheus.Metric) {

	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	// create a list to hold our loadbalancers
	response, err := c.lbClient.ListLBs(&lb.ListLBsRequest{})

	if err != nil {
		c.errors.WithLabelValues("loadbalancer").Add(1)
		_ = level.Warn(c.logger).Log("msg", "can't fetch the list of loadbalancers", "err", err)

		return
	}

	_ = level.Debug(c.logger).Log("msg", fmt.Sprintf("found %d loadbalancer instances", len(response.LBs)))

	var wg sync.WaitGroup
	defer wg.Wait()

	for _, loadbalancer := range response.LBs {

		wg.Add(1)

		_ = level.Debug(c.logger).Log("msg", fmt.Sprintf("Fetching metrics for loadbalancer : %s", loadbalancer.Name))

		go c.FetchLoadbalancerMetrics(&wg, ch, loadbalancer)
	}
}

func (c *LoadBalancerCollector) FetchLoadbalancerMetrics(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, loadbalancer *lb.LB) {

	defer parentWg.Done()

	labels := []string{
		loadbalancer.ID,
		loadbalancer.Name,
		loadbalancer.Region.String(),
		loadbalancer.Type,
	}

	// TODO check if it is possible to add loadbalancer tag as labels
	//for _, tags := range instance.Tags {
	//	labels = append(labels, tags)
	//}

	var active float64

	switch loadbalancer.Status {
	case lb.LBStatusReady:
		active = 1.0
	case lb.LBStatusMigrating:
		active = 0.5
	default:
		active = 0.0
	}

	ch <- prometheus.MustNewConstMetric(c.Up, prometheus.GaugeValue, active, labels...)

	query := url.Values{}

	query.Add("start_date", time.Now().Add(-1*time.Hour).Format(time.RFC3339))
	query.Add("end_date", time.Now().Format(time.RFC3339))

	scwReq := &scw.ScalewayRequest{
		Method:  "GET",
		Path:    "/lb-private/v1/regions/" + fmt.Sprint(loadbalancer.Region) + "/lbs/" + fmt.Sprint(loadbalancer.ID) + "/metrics",
		Query:   query,
		Headers: http.Header{},
	}

	var metricResponse LbMetrics

	err := c.client.Do(scwReq, &metricResponse)

	if err != nil {
		c.errors.WithLabelValues("loadbalancer").Add(1)
		_ = level.Warn(c.logger).Log(
			"msg", "can't fetch the metric for the loadbalancer",
			"err", err,
			"loadbalancerId", loadbalancer.ID,
			"loadbalancerName", loadbalancer.Name,
		)

		return
	}

	for _, timeseries := range metricResponse.Timeseries {

		var series *prometheus.Desc

		switch timeseries.Name {
		case "node_network_receive_bits_sec":
			series = c.NetworkReceive
		case "node_network_transmit_bits_sec":
			series = c.NetworkTransmit
		case "current_connection_rate_sec":
			series = c.Connection
		case "current_new_connection_rate_sec":
			series = c.NewConnection
		default:
			_ = level.Debug(c.logger).Log(
				"msg", "unmapped scaleway metric",
				"err", err,
				"loadbalancerId", loadbalancer.ID,
				"loadbalancerName", loadbalancer.Name,
				"scwMetric", timeseries.Name,
			)
			continue
		}

		if len(timeseries.Points) == 0 {
			c.errors.WithLabelValues("database").Add(1)
			_ = level.Warn(c.logger).Log(
				"msg", "no data were returned for the metric",
				"err", err,
				"loadbalancerId", loadbalancer.ID,
				"loadbalancerName", loadbalancer.Name,
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
