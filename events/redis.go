// Package events - publishing of host events and reports to Redis.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/meklis/all-ok-pinger/prom"
	"github.com/meklis/all-ok-pinger/state"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// Messages are published to channels (PUBLISH channel <json>).
const (
	reportChunk = 1000
	batchSize   = 500
)

type Config struct {
	Enabled    bool   `yaml:"enabled"`
	Addr       string `yaml:"addr"`
	Password   string `yaml:"password"`
	DB         int    `yaml:"db"`
	EventsKey  string `yaml:"events_key"`
	ReportsKey string `yaml:"reports_key"`
	QueueSize  int    `yaml:"queue_size"`
}

type message struct {
	key  string
	data []byte
}

type Publisher struct {
	conf   Config
	ident  string
	client *redis.Client
	queue  chan message
	lg     *logrus.Logger
}

func New(conf Config, ident string, lg *logrus.Logger) (*Publisher, error) {
	if conf.Addr == "" {
		return nil, fmt.Errorf("redis.addr is required")
	}
	if conf.QueueSize <= 0 {
		conf.QueueSize = 100000
	}
	p := &Publisher{
		conf:  conf,
		ident: ident,
		lg:    lg,
		queue: make(chan message, conf.QueueSize),
		client: redis.NewClient(&redis.Options{
			Addr:     conf.Addr,
			Password: conf.Password,
			DB:       conf.DB,
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.client.Ping(ctx).Err(); err != nil {
		lg.Warnf("Redis %v is not available now: %v (events will be sent after reconnect)", conf.Addr, err)
	} else {
		lg.Infof("Connected to redis %v, pubsub events channel=%v, reports channel=%v", conf.Addr, conf.EventsKey, conf.ReportsKey)
	}
	go p.worker()
	return p, nil
}

type eventMessage struct {
	Ident string `json:"ident"`
	state.Event
}

// Event - enqueue host event. Never blocks: if queue is full event is dropped
func (p *Publisher) Event(e state.Event) {
	if p.conf.EventsKey == "" {
		return
	}
	data, err := json.Marshal(eventMessage{Ident: p.ident, Event: e})
	if err != nil {
		p.lg.Errorf("Error marshal event: %v", err)
		return
	}
	p.enqueue(message{key: p.conf.EventsKey, data: data})
}

// Report - enqueue report (objects of changed hosts), split by chunks
func (p *Publisher) Report(objects []map[string]interface{}) {
	if p.conf.ReportsKey == "" || len(objects) == 0 {
		return
	}
	now := time.Now()
	for i := 0; i < len(objects); i += reportChunk {
		end := i + reportChunk
		if end > len(objects) {
			end = len(objects)
		}
		data, err := json.Marshal(map[string]interface{}{
			"type":  "report",
			"ident": p.ident,
			"time":  now,
			"count": end - i,
			"items": objects[i:end],
		})
		if err != nil {
			p.lg.Errorf("Error marshal report: %v", err)
			return
		}
		p.enqueue(message{key: p.conf.ReportsKey, data: data})
	}
}

func (p *Publisher) enqueue(m message) {
	select {
	case p.queue <- m:
	default:
		prom.RedisMessage("dropped", 1)
	}
}

func (p *Publisher) worker() {
	batch := make([]message, 0, batchSize)
	for m := range p.queue {
		batch = append(batch[:0], m)
	fill:
		for len(batch) < batchSize {
			select {
			case m := <-p.queue:
				batch = append(batch, m)
			default:
				break fill
			}
		}
		for attempt := 1; ; attempt++ {
			err := p.send(batch)
			if err == nil {
				prom.RedisMessage("sent", len(batch))
				break
			}
			if attempt >= 3 {
				p.lg.Errorf("Error publish %v messages to redis, dropped: %v", len(batch), err)
				prom.RedisMessage("error", len(batch))
				break
			}
			time.Sleep(time.Second)
		}
	}
}

func (p *Publisher) send(batch []message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pipe := p.client.Pipeline()
	for _, m := range batch {
		pipe.Publish(ctx, m.key, m.data)
	}
	_, err := pipe.Exec(ctx)
	return err
}
