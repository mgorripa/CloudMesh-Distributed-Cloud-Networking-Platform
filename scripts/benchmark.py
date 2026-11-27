#!/usr/bin/env python3
"""Simple HTTP benchmark; report raw measurements rather than presumed speedups."""
import argparse
import concurrent.futures
import json
import math
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument("--requests", type=int, default=500)
    parser.add_argument("--workers", type=int, default=16)
    args = parser.parse_args()
    if args.requests < 1 or args.workers < 1:
        parser.error("requests and workers must be positive")

    def request(_):
        started = time.perf_counter()
        try:
            with urllib.request.urlopen(args.url, timeout=5) as response:
                response.read()
                ok = 200 <= response.status < 300
        except (OSError, ValueError):
            ok = False
        return ok, (time.perf_counter() - started) * 1000

    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as executor:
        results = list(executor.map(request, range(args.requests)))
    elapsed = time.perf_counter() - started
    latencies = sorted(latency for ok, latency in results if ok)
    def percentile(p):
        return round(latencies[max(0, math.ceil(p * len(latencies)) - 1)], 3) if latencies else None
    print(json.dumps({"url": args.url, "requests": args.requests, "workers": args.workers,
                      "successful": len(latencies), "failed": args.requests - len(latencies),
                      "successful_rps": round(len(latencies) / elapsed, 2),
                      "p50_ms": percentile(.5), "p95_ms": percentile(.95), "p99_ms": percentile(.99)}, indent=2))


if __name__ == "__main__":
    main()
