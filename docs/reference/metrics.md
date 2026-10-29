# Metrics

The node plugin and controller serve Prometheus `/metrics` on port `metrics.port` (default 9809), behind headless `<release>-node-metrics` and `<release>-controller-metrics` Services. `metrics.serviceMonitor.enabled` adds a ServiceMonitor (needs the `monitoring.coreos.com/v1` CRDs). The endpoints are unauthenticated plain HTTP. Mount Pods serve no metrics: mergerfs has none.

| Series | Type | Labels | Component |
|---|---|---|---|
| `csi_mergerfs_requests_total` | counter | `method`, `code` | node, controller |
| `csi_mergerfs_request_duration_seconds` | histogram | `method` | node, controller |
| `csi_mergerfs_pools` | gauge | `state` = `starting`, `ready`, `lost` | node |
| `csi_mergerfs_mount_healthy` | gauge | `id`, `volume_id` | node |
| `csi_mergerfs_mount_lost_total` | counter | | node |
| `csi_mergerfs_adoptions_total` | counter | `result` = `adopted`, `cleaned` | node |
| `csi_mergerfs_workload_heals_total` | counter | `result` = `evicted`, `skipped`, `blocked` | node |

Plus the Go runtime and process collectors. Labels carry volume IDs and pool ids, never branch paths or options.

Alert ideas: `increase(csi_mergerfs_mount_lost_total[10m]) > 0`; `csi_mergerfs_pools{state="lost"} > 0` for longer than the consumers need to restart.
