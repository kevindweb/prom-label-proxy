package querymw

import (
	"context"
)

// Mocker simply mocks the main ThanosQuerier methods for unit testing
type Mocker struct {
	QueryInstantFunc func(context.Context, InstantRequest) error
	QueryRangeFunc   func(context.Context, RangeRequest) error
}

var _ ThanosClient = &Mocker{}

func (s *Mocker) QueryInstant(ctx context.Context, r InstantRequest) error {
	return s.QueryInstantFunc(ctx, r)
}

func (s *Mocker) QueryRange(ctx context.Context, r RangeRequest) error {
	return s.QueryRangeFunc(ctx, r)
}
