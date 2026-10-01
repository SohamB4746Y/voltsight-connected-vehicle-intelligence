// Command vstopics creates the platform's Kafka topics (idempotent).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"voltsight/internal/kafkautil"
)

func main() {
	brokers := flag.String("brokers", "127.0.0.1:29092", "comma-separated seed brokers")
	rf := flag.Int("rf", 1, "replication factor (1 for the single-broker core profile, 3 for ha)")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	created, err := kafkautil.Ensure(ctx, strings.Split(*brokers, ","), int16(*rf), kafkautil.Specs())
	if err != nil {
		fmt.Fprintln(os.Stderr, "vstopics:", err)
		os.Exit(1)
	}
	fmt.Printf("topics ready (created %d: %v)\n", len(created), created)
}
