package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/order"
	"github.com/openzot/openzot/internal/watch"
	"github.com/openzot/openzot/tui"
)

// watchRunner dispatches watched orders. Each one gets exactly the treatment a
// batch position does - ledger skip, automatic continuation of an unfinished
// run, its own session log, its own recorded outcome - and whatever happens to
// one order, the watch stays up.
type watchRunner struct {
	runs oneRun
}

// newWatchRunner wires a dispatcher to the resolved command line. run is the
// engine entry point, split out so tests can inject a fake and never reach a
// provider.
func newWatchRunner(ctx context.Context, cfg zot.Config, sessions string, ledger order.Ledger, rerun, fresh bool) watchRunner {
	return watchRunner{
		runs: oneRun{
			ctx:      ctx,
			cfg:      cfg,
			sessions: sessions,
			ledger:   ledger,
			rerun:    rerun,
			fresh:    fresh,
			run:      zot.RunWith,
		},
	}
}

// Dispatch runs one picked-up order. An order that does not settle is reported
// and the watch moves on - a factory pointed at a folder cannot let 3am's
// failed order end 4am's good one - and only a deliberate stop ends the watch:
// stopping is the operator's decision, not an order failing.
func (w watchRunner) Dispatch(o order.Order) {
	if err := w.runs.execute(o, true); err != nil {
		if errors.Is(err, tui.ErrCancelled) || errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "zot: watch: stopped while %s was running\n", o.Path)

			return
		}

		fmt.Fprintf(os.Stderr, "zot: watch: order %s did not settle (%v); staying on watch\n", o.Path, err)
	}
}

// startWatch runs the watch loop until its context is cancelled. It is a
// variable so tests can observe what run() wires up without starting a live
// watcher; the default builds the real one against the given target.
var startWatch = func(ctx context.Context, target string, d watch.Dispatcher) error {
	mode, err := watch.New(target, d, watch.WithOutput(os.Stderr))
	if err != nil {
		return err
	}

	mode.Run(ctx)

	return nil
}
