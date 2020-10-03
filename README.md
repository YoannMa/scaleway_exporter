# Scaleway Exporter

Prometheus exporter for various metrics about your [Scaleway Elements](https://www.scaleway.com/en/elements/) loadbalancers and managed databases, written in Go.

## TODO

- [ ] Add more documentation
- [ ] Example prometheus rules
- [ ] Example grafana dashboard
- [ ] Proper CI
- [ ] Cross Region metrics pulling
- [ ] More metrics ? (Container Registry size is available)
- [ ] Ability to filter the kind of product (only database for example)
- [ ] Register a new default port as it's using one from [another Scaleway Exporter](https://github.com/promhippie/scw_exporter) ? (see [prometheus documentation](https://github.com/prometheus/prometheus/wiki/Default-port-allocations))

## Acknowledgements

This exporter is **heavily** inspired by the one for [DigitalOcean](https://github.com/metalmatze/digitalocean_exporter)