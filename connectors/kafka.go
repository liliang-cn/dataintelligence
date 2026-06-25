package connectors

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"
)

// KafkaSource consumes up to Max messages from a topic (from the beginning) and
// flattens JSON payloads into Records. Generic streaming adapter — it also backs
// the change-stream view of an event log.
type KafkaSource struct {
	Brokers string // comma-separated host:port
	Topic   string
	Max     int
}

func (s *KafkaSource) reader() *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:   strings.Split(s.Brokers, ","),
		Topic:     s.Topic,
		Partition: 0,
		MinBytes:  1,
		MaxBytes:  10e6,
	})
}

func (s *KafkaSource) Discover(_ context.Context) (SourceSchema, error) {
	return SourceSchema{Name: "kafka:" + s.Topic, Fields: []Field{{Name: "_offset", Type: "int"}, {Name: "_key", Type: "text"}}}, nil
}

// Read consumes from the start of the topic up to Max messages (bounded), parsing
// each JSON value into a Record.
func (s *KafkaSource) Read(ctx context.Context) (Batch, error) {
	r := s.reader()
	defer r.Close()
	if err := r.SetOffset(kafka.FirstOffset); err != nil {
		return Batch{}, err
	}
	max := s.Max
	if max <= 0 {
		max = 100
	}
	batch := Batch{Schema: SourceSchema{Name: "kafka:" + s.Topic}}
	seen := map[string]bool{"_offset": true, "_key": true}
	for i := 0; i < max; i++ {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		m, err := r.ReadMessage(c)
		cancel()
		if err != nil {
			break // drained (timeout) or end
		}
		rec := Record{"_offset": itoa(m.Offset), "_key": string(m.Key)}
		var payload map[string]any
		if json.Unmarshal(m.Value, &payload) == nil {
			for k, v := range payload {
				rec[k] = str(v)
				seen[k] = true
			}
		} else {
			rec["value"] = string(m.Value)
			seen["value"] = true
		}
		batch.Rows = append(batch.Rows, rec)
	}
	for k := range seen {
		batch.Schema.Fields = append(batch.Schema.Fields, Field{Name: k, Type: "text"})
	}
	return batch, nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
