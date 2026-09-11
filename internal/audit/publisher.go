package audit

import "context"

type Publisher interface {
	Publish(context.Context, Event) error
	Healthy() bool
	Close() error
}
