package main

import (
	"net/http"
	"os"
	"runtime"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"github.com/yoannma/scaleway_exporter/collector"
)

var (
	// Version of scaleway_exporter.
	Version string
	// Revision or Commit this binary was built from.
	Revision string
	// BuildDate this binary was built.
	BuildDate string
	// GoVersion running this binary.
	GoVersion = runtime.Version()
	// StartTime has the time this was started.
	StartTime = time.Now()
)

// Config gets its content from env and passes it on to different packages
type Config struct {
	Debug                        bool       `arg:"env:DEBUG"`
	ScalewayAccessKey            string     `arg:"env:SCALEWAY_ACCESS_KEY"`
	ScalewaySecretKey            string     `arg:"env:SCALEWAY_SECRET_KEY"`
	ScalewayRegion               scw.Region `arg:"env:SCALEWAY_REGION"`
	HTTPTimeout                  int        `arg:"env:HTTP_TIMEOUT"`
	WebAddr                      string     `arg:"env:WEB_ADDR"`
	WebPath                      string     `arg:"env:WEB_PATH"`
	DisableBucketCollector       bool       `arg:"--disable-bucket-collector"`
	DisableDatabaseCollector     bool       `arg:"--disable-database-collector"`
	DisableLoadBalancerCollector bool       `arg:"--disable-loadbalancer-collector"`
}

func main() {
	_ = godotenv.Load()

	c := Config{
		HTTPTimeout:                  5000,
		WebPath:                      "/metrics",
		WebAddr:                      ":9503",
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
		_ = level.Error(logger).Log("msg", "Scaleway Access Key is required", "err")
		os.Exit(1)
	}

	if c.ScalewaySecretKey == "" {
		_ = level.Error(logger).Log("msg", "Scaleway Secret Key is required", "err")
		os.Exit(1)
	}

	var regions []scw.Region
	if c.ScalewayRegion == "" {
		_ = level.Info(logger).Log("msg", "Scaleway Region is set to ALL")
		regions = scw.AllRegions
	} else {
		regions = []scw.Region{scw.Region(c.ScalewayRegion)}
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
	r.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	r.MustRegister(prometheus.NewGoCollector())
	r.MustRegister(errors)
	r.MustRegister(collector.NewExporterCollector(logger, Version, Revision, BuildDate, GoVersion, StartTime))

	if !c.DisableDatabaseCollector {
		r.MustRegister(collector.NewDatabaseCollector(logger, errors, client, timeout, regions))
	}
	if !c.DisableBucketCollector {
		r.MustRegister(collector.NewBucketCollector(logger, errors, client, timeout, regions))
	}
	if !c.DisableLoadBalancerCollector {
		r.MustRegister(collector.NewLoadBalancerCollector(logger, errors, client, timeout, regions))
	}

	http.Handle(c.WebPath,
		promhttp.HandlerFor(r, promhttp.HandlerOpts{}),
	)

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
	if err := http.ListenAndServe(c.WebAddr, nil); err != nil {
		_ = level.Error(logger).Log("msg", "http listenandserve error", "err", err)
		os.Exit(1)
	}
}
