package clock

import (
	"context"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type Source = clock.Source

func FromContext(ctx context.Context) (Source, error) {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	return p.Resources.Time()
}

func Now(ctx context.Context) (Time, error) {
	source, err := FromContext(ctx)
	if err != nil {
		return Time{}, err
	}
	return now(source, ctx)
}

func now(source Source, ctx context.Context) (Time, error) {
	nanoseconds, err := source.Wall(ctx)
	if err != nil {
		return Time{}, err
	}
	return timeFromUnixNano(nanoseconds), nil
}

func Since(ctx context.Context, t Time) (Duration, error) {
	current, err := Now(ctx)
	if err != nil {
		return 0, err
	}
	return current.Sub(t), nil
}

func Sleep(ctx context.Context, d Duration) error {
	if d <= 0 {
		return nil
	}
	source, err := FromContext(ctx)
	if err != nil {
		return err
	}
	return source.Sleep(ctx, uint64(d))
}

var ErrNoTimeZoneData = fmt.Errorf("客体没有时区数据库：只提供 UTC 与固定偏移时区")
