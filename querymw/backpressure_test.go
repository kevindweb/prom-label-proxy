package querymw

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestBackpressureRaceCondition(t *testing.T) {
	minWindow, maxWindow := 1, 10
	baseURL, _ := url.Parse("http://localhost:9090")
	ctx := context.Background()
	q := "q"
	tim := time.Time{}
	errs := []error{}
	var mu sync.Mutex
	addError := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, err)
	}

	expectedErr := errors.New("should drop watermark")
	client := &Mocker{
		QueryInstantFunc: func(_ context.Context, _ InstantRequest) error {
			return nil
		},
		QueryRangeFunc: func(_ context.Context, _ RangeRequest) error {
			return expectedErr
		},
	}
	querier := NewBackpressure(client, minWindow, maxWindow, nil, "")

	var overallWaitGroup sync.WaitGroup
	overallWaitGroup.Add(3)
	var wg1 sync.WaitGroup
	wg1.Add(1)
	go func() {
		querier.mu.Lock()
		if querier.max != 10 {
			addError(errors.New("max should start at 10"))
		}
		querier.mu.Unlock()

		req := InstantRequest{
			Base:  baseURL,
			Query: q,
			Time:  tim,
		}
		querier.QueryInstant(ctx, req)
		querier.QueryInstant(ctx, req)
		querier.QueryInstant(ctx, req)
		querier.QueryInstant(ctx, req)
		querier.QueryInstant(ctx, req)
		querier.mu.Lock()
		if querier.active != 0 {
			addError(errors.New("no queries should be active"))
		}
		if querier.watermark != 6 {
			addError(fmt.Errorf(
				"should have incremented by one each time, instead got %d",
				querier.watermark,
			))
		}
		querier.mu.Unlock()
		overallWaitGroup.Done()
		wg1.Done()
	}()

	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		wg1.Wait()
		querier.mu.Lock()
		if querier.max != 10 {
			addError(errors.New("max should still be at 10"))
		}
		querier.mu.Unlock()

		req := RangeRequest{
			Base:  baseURL,
			Query: q,
			Start: tim,
			End:   tim,
			Step:  time.Second,
		}
		err := querier.QueryRange(ctx, req)
		if !errors.Is(err, expectedErr) {
			addError(fmt.Errorf("got %v, expectedErr %v", err, expectedErr))
		}
		overallWaitGroup.Done()
		wg2.Done()
	}()

	go func() {
		wg2.Wait()
		req := InstantRequest{
			Base:  baseURL,
			Query: q,
			Time:  tim,
		}
		querier.QueryInstant(ctx, req)
		querier.mu.Lock()
		if querier.active != 0 {
			addError(errors.New("no queries should be active from the final querier"))
		}
		if querier.watermark != 4 {
			addError(fmt.Errorf("watermark should be 4, instead got %d", querier.watermark))
		}
		overallWaitGroup.Done()
	}()

	overallWaitGroup.Wait()
	if len(errs) != 0 {
		t.Fatalf("errors should be nil %v", errs)
	}
}

func TestBackpressureEdgeCases(t *testing.T) {
	minWindow, maxWindow := 1, 10
	ctx := context.Background()

	client := &Mocker{
		QueryInstantFunc: func(_ context.Context, _ InstantRequest) error {
			return nil
		},
		QueryRangeFunc: func(_ context.Context, _ RangeRequest) error {
			return errors.New("fail")
		},
	}
	querier := NewBackpressure(client, minWindow, maxWindow, nil, "")
	if querier.max != 10 {
		t.Fatal("max should start at 10")
	}

	req := InstantRequest{}
	querier.QueryInstant(ctx, req)
	querier.QueryInstant(ctx, req)
	querier.QueryInstant(ctx, req)
	querier.QueryInstant(ctx, req)
	querier.QueryInstant(ctx, req)
	if querier.active != 0 {
		t.Fatal("no queries should be active")
	}
	if querier.watermark != 6 {
		t.Fatalf("should have incremented by one each time, instead got %d", querier.watermark)
	}

	querier.QueryRange(ctx, RangeRequest{})
	if querier.watermark != 3 {
		t.Fatalf("should have scaled by half to 3, instead got %d", querier.watermark)
	}

	querier.QueryRange(ctx, RangeRequest{})
	querier.QueryRange(ctx, RangeRequest{})
	querier.QueryRange(ctx, RangeRequest{})
	querier.QueryRange(ctx, RangeRequest{})
	if querier.watermark != 1 {
		t.Fatalf("should not get below min window size of 1, instead got %d", querier.watermark)
	}
}
