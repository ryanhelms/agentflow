package bus

import (
	"context"
	"fmt"
	"time"
)

// Handle runs h for each event of sub until ctx is done or sub closes, as an
// Event-Driven Consumer. An error or panic from h becomes a Letter on dl
// (when dl is not nil) and Handle moves on to the next event, so one bad
// event never stops a consumer.
func Handle(ctx context.Context, sub Subscription, consumer string, h Handler, dl DeadLetter) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-sub.Events():
			if !ok {
				return nil
			}
			if err := call(ctx, h, e); err != nil && dl != nil {
				dl.Dead(ctx, Letter{Event: e, Consumer: consumer, Reason: err.Error(), Time: time.Now().UTC()})
			}
		}
	}
}

func call(ctx context.Context, h Handler, e Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return h(ctx, e)
}
