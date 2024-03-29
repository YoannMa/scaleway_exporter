package main

import (
	"net/http"
	"os"
	"runtime"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"github.com/yoannma/scaleway_exporter/collector"
)

var (
	// Version of this binary.
	Version string //nolint:gochecknoglobals // LDFlags

	// Revision or Commit this binary was built from.
	Revision string //nolint: gochecknoglobals // LDFlags

	// BuildDate this binary was built.
	BuildDate string //nolint:gochecknoglobals // LDFlags

	// GoVersion running this binary.
	GoVersion = runtime.Version() //nolint:gochecknoglobals // LDFlags

	// StartTime has the time this was started.
	StartTime = time.Now() //nolint:gochecknoglobals // LDFlags
)

// Config gets its content from env and passes it on to different packages.
type Config struct {
	Debug                        bool       `arg:"env:DEBUG"`
	ScalewayAccessKey            string     `arg:"env:SCALEWAY_ACCESS_KEY"`
	ScalewaySecretKey            string     `arg:"env:SCALEWAY_SECRET_KEY"`
	ScalewayRegion               scw.Region `arg:"env:SCALEWAY_REGION"`
	ScalewayZone                 scw.Zone   `arg:"env:SCALEWAY_ZONE"`
	ScalewayOrganizationID       string     `arg:"env:SCALEWAY_ORGANIZATION_ID"`
	HTTPTimeout                  int        `arg:"env:HTTP_TIMEOUT"`
	WebAddr                      string     `arg:"env:WEB_ADDR"`
	WebPath                      string     `arg:"env:WEB_PATH"`
	DisableBillingCollector      bool       `arg:"--disable-billing-collector"`
	DisableBucketCollector       bool       `arg:"--disable-bucket-collector"`
	DisableDatabaseCollector     bool       `arg:"--disable-database-collector"`
	DisableLoadBalancerCollector bool       `arg:"--disable-loadbalancer-collector"`
	DisableRedisCollector        bool       `arg:"--disable-redis-collector"`
}

func main() {
	_ = godotenv.Load()

	c := Config{
		HTTPTimeout:                  5000,
		WebPath:                      "/metrics",
		WebAddr:                      ":9503",
		DisableBillingCollector:      false,
		DisableBucketCollector:       false,
		DisableDatabaseCollector:     false,
		DisableLoadBalancerCollector: false,
	}
	arg.MustParse(&c)

	filterOption := level.AllowInfo()
	if c.Debug {
		filterOption = level.AllowDebug()
	}

	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	logger = level.NewFilter(logger, filterOption)
	logger = log.With(logger,
		"ts", log.DefaultTimestampUTC,
		"caller", log.DefaultCaller,
	)

	if c.ScalewayAccessKey == "" {
		_ = level.Error(logger).Log("msg", "Scaleway Access Key is required")
		os.Exit(1)
	}

	if c.ScalewaySecretKey == "" {
		_ = level.Error(logger).Log("msg", "Scaleway Secret Key is required")
		os.Exit(1)
	}

	var regions []scw.Region
	if c.ScalewayRegion == "" {
		_ = level.Info(logger).Log("msg", "Scaleway Region is set to ALL")
		regions = scw.AllRegions
	} else {
		regions = []scw.Region{c.ScalewayRegion}
	}

	var zones []scw.Zone
	if c.ScalewayZone == "" {
		_ = level.Info(logger).Log("msg", "Scaleway Zone is set to ALL")
		zones = scw.AllZones
	} else {
		zones = []scw.Zone{c.ScalewayZone}
	}

	_ = level.Info(logger).Log(
		"msg", "starting scaleway_exporter",
		"version", Version,
		"revision", Revision,
		"buildDate", BuildDate,
		"goVersion", GoVersion,
	)

	client, err := scw.NewClient(
		// Get your credentials at https://console.scaleway.com/account/credentials
		scw.WithDefaultRegion(regions[0]),
		scw.WithAuth(c.ScalewayAccessKey, c.ScalewaySecretKey),
	)

	if err != nil {
		_ = level.Error(logger).Log("msg", "Scaleway client initialization error", "err", err)
		os.Exit(1)
	}

	timeout := time.Duration(c.HTTPTimeout) * time.Millisecond

	errors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scaleway_errors_total",
		Help: "The total number of errors per collector",
	}, []string{"collector"})

	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(errors)
	r.MustRegister(collector.NewExporterCollector(logger, Version, Revision, BuildDate, GoVersion, StartTime))

	if !c.DisableBillingCollector && c.ScalewayOrganizationID != "" {
		r.MustRegister(collector.NewBillingCollector(logger, errors, client, timeout, c.ScalewayOrganizationID))
	}

	if !c.DisableBucketCollector {
		r.MustRegister(collector.NewBucketCollector(logger, errors, client, timeout, regions))
	}

	if !c.DisableDatabaseCollector {
		r.MustRegister(collector.NewDatabaseCollector(logger, errors, client, timeout, regions))
	}

	if !c.DisableLoadBalancerCollector {
		r.MustRegister(collector.NewLoadBalancerCollector(logger, errors, client, timeout, zones))
	}

	if !c.DisableRedisCollector {
		r.MustRegister(collector.NewRedisCollector(logger, errors, client, timeout, zones))
	}

	http.Handle(c.WebPath, promhttp.HandlerFor(r, promhttp.HandlerOpts{}))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>
			<head><title>Scaleway Exporter</title></head>
			<body>
			<h1>Scaleway Exporter</h1>
			<p><a href="` + c.WebPath + `">Metrics</a></p>
			</body>
			</html>`))
	})

	_ = level.Info(logger).Log("msg", "listening", "addr", c.WebAddr)

	server := &http.Server{
		Addr:              c.WebAddr,
		ReadHeaderTimeout: 5 * time.Second,
	}

	err = server.ListenAndServe()

	if err != nil {
		_ = level.Error(logger).Log("msg", "http ListenAndServe error", "err", err)

		os.Exit(1)
	}
}
