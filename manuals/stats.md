# Stats

Charts covering how many containers DoUpRo tracks over time, how many
updates each container/stack has received per week, how updates and
rollbacks have gone (success vs. failed vs. rolled back), and notification
delivery success by channel.

## Prometheus

If you already run Prometheus, DoUpRo exposes the same data (and more) at
`/metrics` in standard Prometheus format. Add it as a scrape target like
any other exporter:

```yaml
scrape_configs:
  - job_name: doupro
    scrape_interval: 30s
    static_configs:
      - targets: ["doupro.home.arpa:8080"]
```

Then in Grafana, a few panel queries to start from:

```promql
doupro_updates_available                      # pending updates, by stack
histogram_quantile(0.95, rate(doupro_update_duration_seconds_bucket[1h]))  # p95 update time
increase(doupro_rollbacks_total{trigger="auto"}[15m]) > 0   # alert: an auto-rollback just happened
```

The full metric list (names, labels, help text) is self-described at
`/metrics` itself — every metric has a `# HELP` line — for anyone wiring
up more panels or alerts.

## Elastic / ELK

`/metrics` is a standard Prometheus text-format endpoint, so it also works
directly with Elastic's own [Prometheus
integration](https://www.elastic.co/docs/reference/integrations/prometheus)
— point Elastic Agent's Prometheus input at
`http://<doupro-host>:8080/metrics` (the `hosts`/`Metrics Path` settings in
that integration) and DoUpRo's metrics land in Elasticsearch alongside
everything else you monitor there. No DoUpRo-side configuration needed —
same endpoint either way.
