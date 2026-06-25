//go:build ignore

// Example seed: produce realistic Meridian order events to the Redpanda topic
// the manifest's `order_events` source reads. Run from the repo root:
//
//	go run examples/meridian/seed/kafka_produce.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

func main() {
	w := &kafka.Writer{
		Addr:                   kafka.TCP("localhost:39094"),
		Topic:                  "order-events",
		Balancer:               &kafka.LeastBytes{},
		AllowAutoTopicCreation: true,
	}
	defer w.Close()

	channels := []string{"web", "ios", "android"}
	var msgs []kafka.Message
	for i := 1; i <= 25; i++ {
		ev := map[string]any{
			"event":          "order_placed",
			"order_ref":      fmt.Sprintf("WEB-%05d", i),
			"customer_email": fmt.Sprintf("customer-%d@example", i), // overwritten below
			"channel":        channels[i%3],
			"amount":         40 + (i*7)%500,
			"ts":             time.Date(2025, time.Month(i%12+1), i%27+1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
		b, _ := json.Marshal(ev)
		msgs = append(msgs, kafka.Message{Key: []byte(ev["order_ref"].(string)), Value: b})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		panic(err)
	}
	fmt.Printf("produced %d order events to order-events\n", len(msgs))
}
