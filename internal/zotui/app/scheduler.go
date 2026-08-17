package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/zotui/store"
)

// RunScheduler starts due workers once per minute until ctx is cancelled.
func (a *App) RunScheduler(ctx context.Context) {
	a.runDue(ctx, time.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			a.runDue(ctx, now)
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) runDue(ctx context.Context, now time.Time) {
	workers, err := a.store.ListWorkers(ctx)
	if err != nil {
		return
	}
	for _, worker := range workers {
		if worker.Schedule.Cron == "" || !cronMatches(worker.Schedule.Cron, worker.Schedule.Timezone, now) {
			continue
		}
		runs, err := a.store.ListRuns(ctx, worker.ID)
		if err != nil || alreadyRanThisMinute(runs, now, worker.Schedule.Timezone) {
			continue
		}
		active := false
		for _, run := range runs {
			if !run.Status.Terminal() {
				active = true
				break
			}
		}
		if active {
			continue
		}
		id, err := a.StartRun(ctx, worker.ID)
		if err == nil && worker.Schedule.RuntimeMinutes > 0 {
			go a.stopAfter(ctx, id, time.Duration(worker.Schedule.RuntimeMinutes)*time.Minute)
		}
	}
}

func (a *App) stopAfter(ctx context.Context, id string, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		_ = a.StopRun(context.Background(), id)
	case <-ctx.Done():
	}
}

func alreadyRanThisMinute(runs []store.Run, now time.Time, zone string) bool {
	location, err := time.LoadLocation(zone)
	if err != nil {
		return false
	}
	want := now.In(location).Truncate(time.Minute)
	for _, run := range runs {
		if run.CreatedAt.In(location).Truncate(time.Minute).Equal(want) {
			return true
		}
	}
	return false
}

func validateSchedule(schedule store.Schedule) error {
	if schedule.Cron == "" {
		return nil
	}
	if schedule.Timezone == "" {
		return fmt.Errorf("a schedule timezone is required")
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("invalid schedule timezone %q", schedule.Timezone)
	}
	fields := strings.Fields(schedule.Cron)
	if len(fields) != 5 {
		return fmt.Errorf("cron schedule must have five fields")
	}
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		if _, err := parseCronField(field, ranges[i][0], ranges[i][1]); err != nil {
			return fmt.Errorf("invalid cron field %q: %w", field, err)
		}
	}
	if schedule.RuntimeMinutes < 0 {
		return fmt.Errorf("schedule runtime cannot be negative")
	}
	return nil
}

func cronMatches(expression, zone string, now time.Time) bool {
	if validateSchedule(store.Schedule{Cron: expression, Timezone: zone}) != nil {
		return false
	}
	location, _ := time.LoadLocation(zone)
	now = now.In(location)
	values := []int{now.Minute(), now.Hour(), now.Day(), int(now.Month()), int(now.Weekday())}
	fields := strings.Fields(expression)
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, field := range fields {
		allowed, _ := parseCronField(field, ranges[i][0], ranges[i][1])
		if !allowed[values[i]] && !(i == 4 && values[i] == 0 && allowed[7]) {
			return false
		}
	}
	return true
}

func parseCronField(field string, minValue, maxValue int) (map[int]bool, error) {
	allowed := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("bad step")
			}
		}
		start, end := minValue, maxValue
		if base != "*" {
			if left, right, rangeValue := strings.Cut(base, "-"); rangeValue {
				var err error
				start, err = strconv.Atoi(left)
				if err != nil {
					return nil, fmt.Errorf("bad range")
				}
				end, err = strconv.Atoi(right)
				if err != nil {
					return nil, fmt.Errorf("bad range")
				}
			} else {
				var err error
				start, err = strconv.Atoi(base)
				if err != nil {
					return nil, fmt.Errorf("not a number")
				}
				end = start
			}
		}
		if start < minValue || end > maxValue || start > end {
			return nil, fmt.Errorf("outside %d-%d", minValue, maxValue)
		}
		for value := start; value <= end; value += step {
			allowed[value] = true
		}
	}
	return allowed, nil
}
