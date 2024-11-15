package querymw

import (
	"context"
	"math/rand"
	"time"
)

// Jitterer sleeps for a random amount of jitter before passing the request through.
type Jitterer struct {
	delay  time.Duration
	client ThanosClient
}

var _ ThanosClient = &Jitterer{}

func NewJitterer(querier ThanosClient, delay time.Duration) *Jitterer {
	return &Jitterer{
		delay:  delay,
		client: querier,
	}
}

func (jq *Jitterer) QueryInstant(ctx context.Context, r InstantRequest) error {
	jq.sleep()
	return jq.client.QueryInstant(ctx, r)
}

func (jq *Jitterer) QueryRange(ctx context.Context, r RangeRequest) error {
	jq.sleep()
	return jq.client.QueryRange(ctx, r)
}

func (jq *Jitterer) sleep() {
	if jq.delay == 0 {
		return
	}

	// nolint:gosec // rand not used for security purposes
	jitter := time.Duration(rand.Intn(int(jq.delay.Nanoseconds())))
	time.Sleep(jitter)
}
