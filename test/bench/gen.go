// Copyright JVQ.net 2026
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// gen writes synthetic JSON Lines logs for the benchmarks: realistic
// application log events of about 450 bytes each, the same bytes every
// time for the same size and seed.
//
//	go run test/bench/gen.go -mb 1024 -out logs.jsonl
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"
)

func main() {
	mb := flag.Int("mb", 1024, "size in MiB")
	out := flag.String("out", "logs.jsonl", "output file")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	rng := rand.New(rand.NewSource(*seed))
	levels := []string{"info", "info", "info", "info", "debug", "debug", "warn", "error"}
	services := []string{"api", "auth", "billing", "search", "db", "queue", "web", "mailer", "cache", "images"}
	methods := []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}
	paths := []string{"/v1/users", "/v1/orders", "/v1/items", "/v2/search", "/health", "/v1/payments", "/login"}
	msgs := []string{"request completed", "cache miss", "slow query", "upstream timeout", "user not found", "token refreshed", "payment declined", "retrying request"}
	uas := []string{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15", "curl/8.4.0", "Go-http-client/2.0", "python-requests/2.31.0"}
	statuses := []int{200, 200, 200, 200, 201, 204, 301, 400, 401, 403, 404, 404, 500, 502, 503}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	limit := int64(*mb) << 20
	var written int64
	var b []byte
	for i := 0; written < limit; i++ {
		ts := start.Add(time.Duration(i) * 7 * time.Millisecond)
		lvl := levels[rng.Intn(len(levels))]
		b = b[:0]
		b = append(b, `{"ts":"`...)
		b = ts.AppendFormat(b, "2006-01-02T15:04:05.000Z")
		b = append(b, `","level":"`...)
		b = append(b, lvl...)
		b = append(b, `","service":"`...)
		b = append(b, services[rng.Intn(len(services))]...)
		b = append(b, `","host":"node-`...)
		b = strconv.AppendInt(b, int64(rng.Intn(64)), 10)
		b = append(b, `","trace_id":"`...)
		b = strconv.AppendUint(b, rng.Uint64(), 16)
		b = append(b, `","request":{"method":"`...)
		b = append(b, methods[rng.Intn(len(methods))]...)
		b = append(b, `","path":"`...)
		b = append(b, paths[rng.Intn(len(paths))]...)
		b = append(b, '/')
		b = strconv.AppendInt(b, int64(rng.Intn(100000)), 10)
		b = append(b, `","ua":"`...)
		b = append(b, uas[rng.Intn(len(uas))]...)
		b = append(b, `"},"status":`...)
		b = strconv.AppendInt(b, int64(statuses[rng.Intn(len(statuses))]), 10)
		b = append(b, `,"latency_ms":`...)
		b = strconv.AppendFloat(b, float64(rng.Intn(500000))/1000, 'f', -1, 64)
		b = append(b, `,"bytes":`...)
		b = strconv.AppendInt(b, int64(rng.Intn(1<<20)), 10)
		b = append(b, `,"user":`...)
		if rng.Intn(4) == 0 {
			b = append(b, "null"...)
		} else {
			b = append(b, `{"id":`...)
			b = strconv.AppendInt(b, int64(rng.Intn(1000000)), 10)
			b = append(b, `,"plan":"`...)
			b = append(b, []string{"free", "pro", "team", "enterprise"}[rng.Intn(4)]...)
			b = append(b, `","country":"`...)
			b = append(b, []string{"US", "DE", "IL", "IN", "BR", "JP", "FR", "GB"}[rng.Intn(8)]...)
			b = append(b, `"}`...)
		}
		b = append(b, `,"tags":[`...)
		for t, n := 0, rng.Intn(4); t < n; t++ {
			if t > 0 {
				b = append(b, ',')
			}
			b = append(b, '"')
			b = append(b, []string{"canary", "beta", "mobile", "eu", "retry", "batch"}[rng.Intn(6)]...)
			b = append(b, '"')
		}
		b = append(b, `],"msg":"`...)
		b = append(b, msgs[rng.Intn(len(msgs))]...)
		if lvl == "error" {
			b = append(b, `","error":{"type":"`...)
			b = append(b, []string{"TimeoutError", "ValueError", "ConnectionReset", "NotFound"}[rng.Intn(4)]...)
			b = append(b, `","stack":"at handler (server.go:`...)
			b = strconv.AppendInt(b, int64(rng.Intn(900)+10), 10)
			b = append(b, `)\n  at main (main.go:42)"}`...)
		} else {
			b = append(b, '"')
		}
		b = append(b, "}\n"...)
		w.Write(b)
		written += int64(len(b))
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f.Close()
}
