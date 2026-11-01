# Monitoring

## On the dashboard

Every machine's card shows its CPU, memory and disk, whether it's on battery, the version it runs and whether it **updates itself**, and in plain words **what's in the way** ("Docker is installed but not running", "keeps failing to start"…). **Machines → Crash reports** lists crashes of sys0 and sys1 anywhere in the pool.

From a terminal on any machine, `sys0 doctor` shows the same, as the background service sees it; `sys0 doctor --fix` starts the container engine if it's stopped.

## Prometheus and Grafana

The node serves its state in Prometheus' format at `/metrics` on the dashboard address. It needs the dashboard key, as a bearer token:

```yaml
scrape_configs:
  - job_name: sys1
    authorization:
      credentials_file: /path/to/sys1/dashboard.key   # in the node's data folder
    static_configs:
      - targets: ["127.0.0.1:7700"]
```

| Metric | Meaning |
|---|---|
| `sys1_build_info{version}` | The node's version |
| `sys1_up_seconds` | Seconds since the node started |
| `sys1_machine_online{machine,os,trust,version}` | 1 while a machine is connected |
| `sys1_machine_cpu_cores`, `sys1_machine_cpu_idle_percent` | CPU |
| `sys1_machine_memory_free_bytes`, `sys1_machine_memory_total_bytes` | Memory |
| `sys1_machine_disk_free_bytes`, `sys1_machine_disk_total_bytes` | Disk |
| `sys1_machine_on_battery` | 1 while paused on battery |
| `sys1_machine_problems` | Problems the machine reports |
| `sys1_machine_auto_update` | 1 if the machine updates itself |
| `sys1_apps{state}` | Apps by state |
| `sys1_app_restarts` | Extra attempts apps needed (crashes and moves) |
| `sys1_crashes` | Distinct crashes seen |

Useful alerts: a machine offline (`sys1_machine_online == 0` for 10 minutes), problems (`sys1_machine_problems > 0`), failed apps (`sys1_apps{state="failed"} > 0`), and machines not updating (`sys1_machine_auto_update == 0`).
