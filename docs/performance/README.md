# Local API performance — 2026-09-21

Loopback only, one Hub process, PostgreSQL 18, HTTP keep-alive. Six operations at 1, 5 and 15 concurrent clients; 30 requests per client. Results include application-error checks, not just HTTP status.

| Operation | Concurrency | Requests | Unexpected failures | p95 ms | p99 ms | Observed req/s |
|---|---:|---:|---:|---:|---:|---:|
| health | 1 | 30 | 0 | 0.247 | 3.215 | 3253.0 |
| health | 5 | 150 | 0 | 1.331 | 1.995 | 6580.0 |
| health | 15 | 450 | 0 | 3.98 | 6.503 | 6629.2 |
| readiness | 1 | 30 | 0 | 4.125 | 4.492 | 285.4 |
| readiness | 5 | 150 | 0 | 8.687 | 11.577 | 719.8 |
| readiness | 15 | 450 | 0 | 26.618 | 30.971 | 785.0 |
| mcp_initialize | 1 | 30 | 0 | 1.186 | 1.491 | 1919.7 |
| mcp_initialize | 5 | 150 | 0 | 3.038 | 3.501 | 2580.8 |
| mcp_initialize | 15 | 450 | 0 | 7.309 | 9.319 | 3420.2 |
| mcp_tools_list | 1 | 30 | 0 | 1.257 | 1.845 | 1592.9 |
| mcp_tools_list | 5 | 150 | 0 | 3.061 | 3.771 | 2651.3 |
| mcp_tools_list | 15 | 450 | 0 | 7.835 | 9.77 | 3259.4 |
| mcp_health | 1 | 30 | 0 | 5.247 | 6.031 | 230.2 |
| mcp_health | 5 | 150 | 0 | 9.873 | 11.263 | 631.3 |
| mcp_health | 15 | 450 | 0 | 29.051 | 32.221 | 716.9 |
| mcp_unauthorized | 1 | 30 | 0 | 0.276 | 0.63 | 5178.6 |
| mcp_unauthorized | 5 | 150 | 0 | 1.26 | 2.311 | 5913.9 |
| mcp_unauthorized | 15 | 450 | 0 | 4.48 | 5.756 | 5822.0 |

3,780 requests. Unauthorized cases expect 401. These short, closed-loop runs are NOT production capacity estimates. Load generator and server share one machine. TLS, network latency and real message workloads are absent. RSS snapshots ranged from 14.9 to 20.0 MiB; snapshots do not establish peak memory or leak freedom.

## Bottleneck

Readiness and MCP health open/close a PostgreSQL connection on every call and verify the schema. They are substantially slower than liveness. Before production, introduce a bounded connection pool and global connection budgets. Autoscaling does not exist yet.

## Not benchmarkable yet

Core channel_settings, update_channel and messages are not installed on a Frappe site. Their mocked contract tests cannot measure database/API performance. Hub message submission, callbacks, template and bulk primitives are covered by local contract tests, but no external Meta, Telegram or email requests were made.

## Fixed-rate readiness checks

15 seconds per phase, at most 15 in-flight requests. Latency includes scheduling delay.

| Target req/s | Sent | Failures | Admission drops | p95 ms | p99 ms |
|---|---:|---:|---:|---:|---:|
| 50 | 750 | 0 | 0 | 4.993 | 6.576 |
| 100 | 1500 | 0 | 0 | 5.272 | 15.815 |

Total across both harnesses: 6,030 requests, zero unexpected failures. This remains a short local test, not a soak, provider-delivery or autoscaling test.
