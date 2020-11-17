package collector

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// BucketCollector collects metrics about all buckets.
type BucketCollector struct {
	logger   log.Logger
	errors   *prometheus.CounterVec
	client   *scw.Client
	region   *scw.Region
	s3Client *s3.S3
	timeout  time.Duration

	ObjectCount          *prometheus.Desc
	Bandwidth            *prometheus.Desc
	StorageUsageStandard *prometheus.Desc
	StorageUsageGlacier  *prometheus.Desc
}

// NewBucketCollector returns a new BucketCollector.
func NewBucketCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration) *BucketCollector {
	errors.WithLabelValues("bucket").Add(0)

	region, _ := client.GetDefaultRegion()

	accessKey, _ := client.GetAccessKey()

	secretKey, _ := client.GetSecretKey()

	newSession, err := session.NewSession(&aws.Config{
		Credentials: credentials.NewStaticCredentials(accessKey, secretKey, ""),
		Region:      aws.String(fmt.Sprint(region)),
	})

	if err != nil {
		level.Error(logger).Log("msg", "can't create a S3 client", "err", err)
		os.Exit(1)
	}

	s3Client := s3.New(newSession, &aws.Config{
		Endpoint:         aws.String("https://s3." + fmt.Sprint(region) + ".scw.cloud"),
		S3ForcePathStyle: aws.Bool(true),
	})

	return &BucketCollector{
		region:   &region,
		logger:   logger,
		errors:   errors,
		client:   client,
		s3Client: s3Client,
		timeout:  timeout,

		ObjectCount: prometheus.NewDesc(
			"scaleway_s3_object_total",
			"Number of objects, excluding parts",
			[]string{"name", "region", "public"}, nil,
		),
		Bandwidth: prometheus.NewDesc(
			"scaleway_s3_bandwidth_bytes",
			"Bucket's Bandwidth usage",
			[]string{"name", "region", "public"}, nil,
		),
		StorageUsageStandard: prometheus.NewDesc(
			"scaleway_s3_storage_usage_standard_bytes",
			"Bucket's Storage usage",
			[]string{"name", "region", "public"}, nil,
		),
		StorageUsageGlacier: prometheus.NewDesc(
			"scaleway_s3_storage_usage_glacier_bytes",
			"Bucket's Storage usage",
			[]string{"name", "region", "public"}, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *BucketCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.ObjectCount
	ch <- c.Bandwidth
	ch <- c.StorageUsageStandard
	ch <- c.StorageUsageGlacier
}

type BucketInfo struct {
	CurrentObjects  int64     `json:"current_objects"`
	CurrentSize     int64     `json:"current_size"`
	CurrentSegments int64     `json:"current_segments"`
	IsPublic        bool      `json:"is_public"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type BucketInfoList struct {
	CurrentObjects int64                 `json:"current_objects"`
	CurrentSize    int64                 `json:"current_size"`
	QuotaBuckets   int64                 `json:"quota_buckets"`
	QuotaObjects   int64                 `json:"quota_objects"`
	QuotaSize      int64                 `json:"quota_size"`
	Buckets        map[string]BucketInfo `json:"buckets"`
}

type BucketInfoRequestBody struct {
	ProjectId   string   `json:"project_id"`
	BucketsName []string `json:"buckets_name"`
}

// InstanceMetrics: instance metrics
type Metric struct {
	// Timeseries: time series of metrics of a given bucket
	Timeseries []*scw.TimeSeries `json:"timeseries"`
}

type MetricName string

const (
	ObjectCount  MetricName = "object_count"
	StorageUsage MetricName = "storage_usage"
	BytesSent    MetricName = "bytes_sent"
)

type HandleSimpleMetricOptions struct {
	Bucket     string
	MetricName MetricName
	Desc       *prometheus.Desc
	labels     []string
}

type HandleMultiMetricsOptions struct {
	Bucket     string
	MetricName MetricName
	DescMatrix map[string]*prometheus.Desc
	labels     []string
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *BucketCollector) Collect(ch chan<- prometheus.Metric) {

	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	buckets, err := c.s3Client.ListBuckets(&s3.ListBucketsInput{})

	if err != nil {
		c.errors.WithLabelValues("bucket").Add(1)
		level.Warn(c.logger).Log("msg", "can't fetch the list of buckets", "err", err)

		return
	}

	scwReq := &scw.ScalewayRequest{
		Method: "POST",
		Path:   "/object-private/v1/regions/" + fmt.Sprint(c.region) + "/buckets-info/",
	}

	var bucketNames []string

	for _, bucket := range buckets.Buckets {

		bucketNames = append(bucketNames, *bucket.Name)
	}

	projectId := strings.Split(*buckets.Owner.ID, ":")[0]

	level.Debug(c.logger).Log("msg", fmt.Sprintf("found %d buckets under projectID %s : %s", len(bucketNames), projectId, bucketNames))

	err = scwReq.SetBody(&BucketInfoRequestBody{ProjectId: projectId, BucketsName: bucketNames})

	var response BucketInfoList

	err = c.client.Do(scwReq, &response)

	if err != nil {
		c.errors.WithLabelValues("bucket").Add(1)
		level.Warn(c.logger).Log("msg", "can't fetch details of buckets", "err", err)

		return
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	for name, bucket := range response.Buckets {

		wg.Add(1)

		level.Debug(c.logger).Log("msg", fmt.Sprintf("Fetching metrics for bucket : %s", name))

		go c.FetchMetricsForBucket(&wg, ch, name, bucket)
	}
}

func (c *BucketCollector) FetchMetricsForBucket(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, name string, bucket BucketInfo) {

	defer parentWg.Done()

	labels := []string{name, fmt.Sprint(c.region), fmt.Sprint(bucket.IsPublic)}

	// TODO check if it is possible to add bucket tag as labels
	//for _, tags := range instance.Tags {
	//	labels = append(labels, tags)
	//}

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(3)

	go c.HandleSimpleMetric(&wg, ch, &HandleSimpleMetricOptions{
		Bucket:     name,
		MetricName: ObjectCount,
		labels:     labels,
		Desc:       c.ObjectCount,
	})

	go c.HandleSimpleMetric(&wg, ch, &HandleSimpleMetricOptions{
		Bucket:     name,
		MetricName: BytesSent,
		labels:     labels,
		Desc:       c.Bandwidth,
	})

	go c.HandleMultiMetrics(&wg, ch, &HandleMultiMetricsOptions{
		Bucket:     name,
		MetricName: StorageUsage,
		labels:     labels,
		DescMatrix: map[string]*prometheus.Desc{"STANDARD": c.StorageUsageStandard, "GLACIER": c.StorageUsageGlacier},
	})
}

func (c *BucketCollector) HandleSimpleMetric(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, options *HandleSimpleMetricOptions) {

	defer parentWg.Done()

	var response Metric

	err := c.FetchMetric(options.Bucket, options.MetricName, &response)

	if err != nil {

		c.errors.WithLabelValues("bucket").Add(1)
		level.Warn(c.logger).Log(
			"msg", "can't fetch the metric "+fmt.Sprint(options.MetricName)+" for the bucket : "+options.Bucket,
			"err", err,
		)

		return
	}

	for _, timeseries := range response.Timeseries {

		sort.Slice(timeseries.Points, func(i, j int) bool {
			return timeseries.Points[i].Timestamp.Before(timeseries.Points[j].Timestamp)
		})

		value := float64(timeseries.Points[len(timeseries.Points)-1].Value)

		ch <- prometheus.MustNewConstMetric(options.Desc, prometheus.GaugeValue, value, options.labels...)
	}

	return
}

func (c *BucketCollector) HandleMultiMetrics(parentWg *sync.WaitGroup, ch chan<- prometheus.Metric, options *HandleMultiMetricsOptions) {

	defer parentWg.Done()

	var response Metric

	err := c.FetchMetric(options.Bucket, options.MetricName, &response)

	if err != nil {

		c.errors.WithLabelValues("bucket").Add(float64(len(options.DescMatrix)))
		level.Warn(c.logger).Log(
			"msg", "can't fetch the metric "+fmt.Sprint(options.MetricName)+" for the bucket : "+options.Bucket,
			"err", err,
		)

		return
	}

	for _, timeseries := range response.Timeseries {

		sort.Slice(timeseries.Points, func(i, j int) bool {
			return timeseries.Points[i].Timestamp.Before(timeseries.Points[j].Timestamp)
		})

		value := float64(timeseries.Points[len(timeseries.Points)-1].Value)

		ch <- prometheus.MustNewConstMetric(options.DescMatrix[timeseries.Metadata["type"]], prometheus.GaugeValue, value, options.labels...)
	}

	return
}

func (c *BucketCollector) FetchMetric(Bucket string, MetricName MetricName, response *Metric) error {

	query := url.Values{}

	query.Add("start_date", time.Now().Add(-1*time.Hour).Format(time.RFC3339))
	query.Add("end_date", time.Now().Format(time.RFC3339))
	query.Add("metric_name", fmt.Sprint(MetricName))

	scwReq := &scw.ScalewayRequest{
		Method: "GET",
		Path:   "/object-private/v1/regions/" + fmt.Sprint(c.region) + "/buckets/" + Bucket + "/metrics",
		Query:  query,
	}

	err := c.client.Do(scwReq, &response)

	if err != nil {

		return err
	}

	return nil
}
