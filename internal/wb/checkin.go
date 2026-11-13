package wb

import (
	"context"
	"errors"
	"math"
	"time"
)

// Checkin is what came of a day's check-in.
type Checkin struct {
	Outcome string  `json:"outcome"` // claimed, done, ineligible, inactive, failed
	Credit  float64 `json:"credit,omitempty"`
	Streak  float64 `json:"streak,omitempty"`
	Message string  `json:"message,omitempty"`
}

type checkinStatus struct {
	Active         bool     `json:"active"`
	TodayCheckedIn *bool    `json:"today_checked_in"`
	TodayCheckedI2 *bool    `json:"todayCheckedIn"`
	TodayCredit    *float64 `json:"today_credit"`
	DailyCredit    *float64 `json:"daily_credit"`
}

func finite(f *float64) (float64, bool) {
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return 0, false
	}
	return *f, true
}

// CheckinOf claims the account's daily credits (China only): the activity
// status read first, transient failures asked again after each of waits.
func CheckinOf(ctx context.Context, d Doer, site *Site, a *Account, waits []time.Duration) (Checkin, error) {
	var status checkinStatus
	if err := Meter(ctx, d, site, a, "/v2/billing/meter/checkin-activity-status", nil, &status); err != nil {
		var me *MeterError
		if errors.As(err, &me) {
			if me.Code == 1001 {
				return Checkin{Outcome: "done"}, nil
			}
			return Checkin{Outcome: "failed", Message: me.Msg}, nil
		}
		return Checkin{}, err
	}
	if !status.Active {
		return Checkin{Outcome: "inactive"}, nil
	}
	checked := status.TodayCheckedIn
	if checked == nil {
		checked = status.TodayCheckedI2
	}
	if checked != nil && *checked {
		out := Checkin{Outcome: "done"}
		cr := status.TodayCredit
		if cr == nil {
			cr = status.DailyCredit
		}
		if n, ok := finite(cr); ok && n > 0 {
			out.Credit = n
		}
		return out, nil
	}
	for attempt := 0; ; attempt++ {
		var got struct {
			Credit     *float64 `json:"credit"`
			StreakDays *float64 `json:"streak_days"`
			Message    any      `json:"message"`
		}
		err := Meter(ctx, d, site, a, "/v2/billing/meter/daily-checkin", nil, &got)
		if err == nil {
			out := Checkin{Outcome: "claimed"}
			if n, ok := finite(got.Credit); ok && n > 0 {
				out.Credit = n
			}
			if n, ok := finite(got.StreakDays); ok && n > 0 {
				out.Streak = n
			}
			if s, ok := got.Message.(string); ok && s != "" {
				out.Message = s
			}
			return out, nil
		}
		var me *MeterError
		if !errors.As(err, &me) {
			return Checkin{}, err
		}
		switch me.Code {
		case 1001:
			return Checkin{Outcome: "done"}, nil
		case 1002:
			return Checkin{Outcome: "ineligible", Message: me.Msg}, nil
		case 1003:
			return Checkin{Outcome: "inactive", Message: me.Msg}, nil
		}
		if me.Status == 401 {
			if Meter(ctx, d, site, a, "/billing/meter/get-user-resource-summary", nil, nil) != nil {
				return Checkin{}, err
			}
			return Checkin{Outcome: "ineligible", Message: "WorkBuddy's check-in takes WorkBuddy's own app"}, nil
		}
		if me.Unreadable {
			return Checkin{Outcome: "failed", Message: "check-in: " + me.Msg}, nil
		}
		transient := me.Status >= 500 || me.Code == 10000
		if transient && attempt < len(waits) {
			select {
			case <-time.After(waits[attempt]):
			case <-ctx.Done():
				return Checkin{}, ctx.Err()
			}
			continue
		}
		return Checkin{Outcome: "failed", Message: me.Msg}, nil
	}
}

// CheckinDay is the Beijing day a check-in counts for.
func CheckinDay(now time.Time) string { return now.In(beijingZone).Format("2006-01-02") }

// Settled says whether a day's earlier answer stands: a claim, done,
// ineligible or inactive does; a failure is asked again after retry.
func (c Checkin) Settled() bool {
	switch c.Outcome {
	case "claimed", "done", "ineligible", "inactive":
		return true
	}
	return false
}
