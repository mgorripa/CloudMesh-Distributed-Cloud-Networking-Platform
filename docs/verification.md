# Verification record

Executed in a Linux x86-64 development sandbox using Go 1.23.2. This is not a measurement of the user's laptop.

| Check | Result |
|---|---|
| `go test -race ./...` | Passed all five tested packages |
| `go vet ./...` | Passed |
| `make build` | Built all three CLI binaries |
| `make demo` | Passed: HTTP distribution, TCP forwarding, backend failover |
| `go test -v ./internal/proxy` | Passed, including real IPv6 loopback forwarding |
| Python benchmark (500 requests, 16 workers) | Raw output in `results/` |
| Kubernetes list/watch | Mock HTTP API test passed; live cluster untested |
| Namespace/VXLAN/iptables lab | Generated plan test passed; privileged execution untested |
| Docker build, kind, hosted GitHub Actions | Not executed: tools unavailable in sandbox |

The local demo recorded six distributed HTTP requests (three to each backend), one TCP-forwarded request, and five successful HTTP requests after stopping one backend. Metrics showed zero proxy errors for this particular run. This is a functional smoke test, not a production availability or scale guarantee. See [raw local demo output](results/local-demo.txt).

The benchmark files [direct backend](results/direct.json) and [proxy](results/proxy.json) are a single local run using a Python client. Do not generalize these results to other machines or claim a speedup; repeat measurements independently and record the environment before including any performance results on a resume.
