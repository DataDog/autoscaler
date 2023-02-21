# Running Integration Tests locally
Included in parallel with `run-e2e.sh` and `deploy-for-e2e.sh` are two alternate versions
with `-locally` as part of their names.  They use Kubernetes in Docker (`kind`) to run a local
cluster in Docker.  Using them will require `docker` and `kind` in your `PATH`.

## External Metrics Tests
The external metrics tests (`recommender-externalmetrics`, available on the `-lacolly` variants)
use a stack of 4 additional programs to support testing:

1. `hack/emit-metrics.py` to generate random CPU and RAM metrics for every pod in the local cluster.
2. Prometheus Pushgateway to accept metrics from `hack/emit-metrics`.
3. Prometheus to store the metrics accepted by the Pushgateway.
4. Prometheus Adapter to provide an External Metrics interface to Prometheus.
