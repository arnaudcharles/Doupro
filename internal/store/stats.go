package store

import "context"

// StatsSummary is a current-state snapshot plus recent-event tallies —
// not a historical time series yet (docs/stats.md describes charts over
// time, which need periodic snapshots this doesn't persist). Shared by
// the API's GET /api/v1/stats and the Stats page so both report the same
// numbers computed the same way.
type StatsSummary struct {
	ContainersTotal     int
	ContainersRunning   int
	UpdatesAvailable    int
	UpdatesSucceeded    int
	UpdatesFailed       int
	RollbacksAuto       int
	RollbacksManual     int
	NotificationsSent   int
	NotificationsFailed int
	SchedulesActive     int
	ContainersByStack   map[string]int
}

// Stats computes a StatsSummary from current tables and the last 500
// events/notifications.
func (s *Store) Stats(ctx context.Context) (StatsSummary, error) {
	summary := StatsSummary{ContainersByStack: map[string]int{}}

	containers, err := s.ListContainers(ctx)
	if err != nil {
		return StatsSummary{}, err
	}
	for _, c := range containers {
		summary.ContainersTotal++
		if c.State == "running" {
			summary.ContainersRunning++
		}
		if c.UpdateAvailable {
			summary.UpdatesAvailable++
		}
		stack := c.Stack
		if stack == "" {
			stack = "Ungrouped"
		}
		summary.ContainersByStack[stack]++
	}

	events, err := s.ListEvents(ctx, EventFilter{Limit: 500})
	if err != nil {
		return StatsSummary{}, err
	}
	for _, e := range events {
		switch e.Type {
		case "update.succeeded":
			summary.UpdatesSucceeded++
		case "update.failed":
			summary.UpdatesFailed++
		case "rollback.auto.succeeded":
			summary.RollbacksAuto++
		case "rollback.manual.succeeded":
			summary.RollbacksManual++
		}
	}

	notifications, err := s.ListNotifications(ctx, 500)
	if err != nil {
		return StatsSummary{}, err
	}
	for _, n := range notifications {
		if n.Status == "sent" {
			summary.NotificationsSent++
		} else {
			summary.NotificationsFailed++
		}
	}

	schedules, err := s.ListSchedules(ctx)
	if err != nil {
		return StatsSummary{}, err
	}
	for _, sc := range schedules {
		if sc.Enabled {
			summary.SchedulesActive++
		}
	}

	return summary, nil
}
