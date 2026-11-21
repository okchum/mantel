package plans

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// parseTime reads RFC 3339 strings and unix seconds or milliseconds.
func parseTime(v any) *time.Time {
	var t time.Time
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return nil
		}
		if n, err := strconv.ParseFloat(x, 64); err == nil {
			return parseTime(n)
		}
		var err error
		if t, err = time.Parse(time.RFC3339Nano, x); err != nil {
			return nil
		}
	case float64:
		if x <= 0 || math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		if x > 1e12 {
			t = time.UnixMilli(int64(x))
		} else {
			t = time.Unix(int64(x), int64((x-math.Floor(x))*1e9))
		}
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		return parseTime(f)
	case int64:
		return parseTime(float64(x))
	case int:
		return parseTime(float64(x))
	default:
		return nil
	}
	t = t.UTC()
	return &t
}

func clampPct(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(0, math.Min(100, v))
}

func humanSpan(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d 天", d/(24*time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%.0f 小时", d.Hours())
	}
	return fmt.Sprintf("%.0f 分钟", d.Minutes())
}
