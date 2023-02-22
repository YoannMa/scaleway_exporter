package collector

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/scaleway/scaleway-sdk-go/api/account/v2"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// BillingCollector collects metrics about all buckets.
type BillingCollector struct {
	logger         log.Logger
	errors         *prometheus.CounterVec
	timeout        time.Duration
	client         *scw.Client
	accountClient  *account.API
	organizationID string

	Consumptions *prometheus.Desc
	Update       *prometheus.Desc
}

// NewBillingCollector returns a new BucketCollector.
func NewBillingCollector(logger log.Logger, errors *prometheus.CounterVec, client *scw.Client, timeout time.Duration, organizationID string) *BillingCollector {
	errors.WithLabelValues("bucket").Add(0)

	_ = level.Info(logger).Log("msg", "Billing collector enabled")

	return &BillingCollector{
		logger:         logger,
		errors:         errors,
		timeout:        timeout,
		client:         client,
		accountClient:  account.NewAPI(client),
		organizationID: organizationID,

		Consumptions: prometheus.NewDesc(
			"scaleway_billing_consumptions",
			"Consumptions",
			[]string{"project_id", "project_name", "category", "operation_path", "description", "currency_code"}, nil,
		),

		Update: prometheus.NewDesc(
			"scaleway_billing_update_timestamp_seconds",
			"Timestamp of the last update",
			nil, nil,
		),
	}
}

// Describe sends the super-set of all possible descriptors of metrics
// collected by this Collector.
func (c *BillingCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.Consumptions
}

type ConsumptionValue struct {
	CurrencyCode string `json:"currency_code"`
	Units        int32  `json:"units"`
	Nanos        uint32 `json:"nanos"`
}

type Consumption struct {
	Description   string           `json:"description"`
	ProjectID     string           `json:"project_id"`
	Category      string           `json:"category"`
	OperationPath string           `json:"operation_path"`
	Value         ConsumptionValue `json:"value"`
}

type BillingResponse struct {
	Consumptions []*Consumption `json:"consumptions"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// Collect is called by the Prometheus registry when collecting metrics.
func (c *BillingCollector) Collect(ch chan<- prometheus.Metric) {
	_, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	response, err := c.accountClient.ListProjects(&account.ListProjectsRequest{OrganizationID: c.organizationID}, scw.WithAllPages())

	if err != nil {
		c.errors.WithLabelValues("billing").Add(1)
		_ = level.Warn(c.logger).Log("msg", "can't fetch the list of projects", "err", err)

		return
	}

	if len(response.Projects) == 0 {
		c.errors.WithLabelValues("billing").Add(1)
		_ = level.Error(c.logger).Log("msg", "No projects were found, perhaps you are missing the 'ProjectManager' permission")

		return
	}

	projects := make(map[string]string)

	for _, project := range response.Projects {
		projects[project.ID] = project.Name
	}

	query := url.Values{}

	query.Set("organization_id", c.organizationID)

	var billingResponse BillingResponse

	err = c.client.Do(&scw.ScalewayRequest{
		Method:  "GET",
		Path:    "/billing/v2alpha1/consumption",
		Query:   query,
		Headers: http.Header{},
	}, &billingResponse)

	if err != nil {
		c.errors.WithLabelValues("billing").Add(1)
		_ = level.Warn(c.logger).Log(
			"msg", "Could not fetch the billing data, perhaps you are missing the 'BillingReadOnly' permission'",
			"err", err,
		)

		return
	}

	for _, consumption := range billingResponse.Consumptions {
		ch <- prometheus.MustNewConstMetric(
			c.Consumptions,
			prometheus.GaugeValue,
			float64(consumption.Value.Units)+float64(consumption.Value.Nanos)/1e9,
			consumption.ProjectID,
			projects[consumption.ProjectID],
			consumption.Category,
			consumption.OperationPath,
			consumption.Description,
			consumption.Value.CurrencyCode,
		)
	}

	ch <- prometheus.MustNewConstMetric(
		c.Update,
		prometheus.GaugeValue,
		float64(billingResponse.UpdatedAt.Unix()),
	)
}
