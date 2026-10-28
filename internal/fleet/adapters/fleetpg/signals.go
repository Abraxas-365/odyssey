package fleetpg

import (
	"context"
	"time"

	"github.com/Abraxas-365/odyssey/internal/errx"
	"github.com/Abraxas-365/odyssey/internal/fleet"
	"github.com/lib/pq"
)

// Signals implements fleet.Signals with LISTEN on a dedicated connection.
type Signals struct{ url string }

// NewSignals listens with its own connection to the database at url.
func NewSignals(url string) *Signals { return &Signals{url: url} }

var _ fleet.Signals = (*Signals)(nil)

// Listen delivers signals until ctx ends. After the connection is lost and
// restored, it calls lost: notifications in between are gone.
func (s *Signals) Listen(ctx context.Context, signal func(fleet.Signal), lost func()) error {
	l := pq.NewListener(s.url, time.Second, 30*time.Second, func(ev pq.ListenerEventType, err error) {
		if ev == pq.ListenerEventReconnected {
			lost()
		}
	})
	defer l.Close()
	if err := l.Listen(Channel); err != nil {
		return errx.Wrap(err, "listen "+Channel, errx.TypeInternal)
	}
	ping := time.NewTicker(time.Minute)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case n := <-l.Notify:
			if n == nil { // reconnected; the event callback already called lost
				continue
			}
			if sig, ok := decodeSignal(n.Extra); ok {
				signal(sig)
			}
		case <-ping.C:
			go func() { _ = l.Ping() }()
		}
	}
}
