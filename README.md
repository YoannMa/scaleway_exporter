# Scaleway Exporter

Prometheus exporter for various metrics about your [Scaleway Elements](https://www.scaleway.com/en/elements/) loadbalancers and managed databases, written in Go.

## How to

```
$ export SCALEWAY_ACCESS_KEY=<access key goes here>
$ export SCALEWAY_SECRET_KEY=<secret key goes here>
$ ./scaleway_exporter
level=info ts=2022-07-19T13:25:40.352520863Z caller=main.go:83 msg="Scaleway Region is set to ALL"
level=info ts=2022-07-19T13:25:40.352550422Z caller=main.go:89 msg="starting scaleway_exporter" version= revision= buildDate= goVersion=go1.18.3
level=info ts=2022-07-19T13:25:40.352691527Z caller=main.go:145 msg=listening addr=:9503
```

By default, all the collectors are enabled (buckets, databases, loadbalancer) over all Scaleway regions and zones.
If needed, you can disable certain collections by adding the `disable-bucket-collector`, `disable-database-collector` or `disable-loadbalancer-collector` flags to the command line.
You can also limit the scraped region by setting the environment variable `SCALEWAY_REGION=fr-par` and the zone with the environment variable `SCALEWAY_ZONE=fr-par-1` for instance.

## TODO

- [ ] Add more documentation
- [ ] Example prometheus rules
- [ ] Example grafana dashboard
- [ ] Proper CI
- [x] Cross Region metrics pulling
- [ ] More metrics ? (Container Registry size is available)
- [x] Ability to filter the kind of product (only database for example)
- [ ] Register a new default port as it's using one from [another Scaleway Exporter](https://github.com/promhippie/scw_exporter) ? (see [prometheus documentation](https://github.com/prometheus/prometheus/wiki/Default-port-allocations))

## Acknowledgements

This exporter is **heavily** inspired by the one for [DigitalOcean](https://github.com/metalmatze/digitalocean_exporter)