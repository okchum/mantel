package collect

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/okchum/mantel/internal/config"
	"github.com/okchum/mantel/internal/state"
)

type openMeteo struct {
	Current struct {
		Temp     float64 `json:"temperature_2m"`
		Feels    float64 `json:"apparent_temperature"`
		Code     int     `json:"weather_code"`
		Wind     float64 `json:"wind_speed_10m"`
		Humidity float64 `json:"relative_humidity_2m"`
		IsDay    int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time []string  `json:"time"`
		Temp []float64 `json:"temperature_2m"`
		Pop  []float64 `json:"precipitation_probability"`
	} `json:"hourly"`
	Daily struct {
		Time []string  `json:"time"`
		Code []int     `json:"weather_code"`
		Max  []float64 `json:"temperature_2m_max"`
		Min  []float64 `json:"temperature_2m_min"`
		Pop  []float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// openMeteoURL is the forecast endpoint (a var so tests can point it elsewhere).
var openMeteoURL = "https://api.open-meteo.com/v1/forecast"

// Weather polls Open-Meteo (no API key needed).
func Weather(ctx context.Context, st *state.Store, w config.Weather, tz string) {
	client := NewClient(ClientOpts{Timeout: 10 * time.Second})
	q := url.Values{}
	q.Set("latitude", fmt.Sprint(w.Latitude))
	q.Set("longitude", fmt.Sprint(w.Longitude))
	q.Set("current", "temperature_2m,apparent_temperature,weather_code,wind_speed_10m,relative_humidity_2m,is_day")
	q.Set("hourly", "temperature_2m,precipitation_probability")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("forecast_days", "4")
	q.Set("timezone", "auto")
	if tz != "" {
		q.Set("timezone", tz)
	}
	u := openMeteoURL + "?" + q.Encode()

	every(ctx, "weather", w.Interval.Or(15*time.Minute), func() {
		var r openMeteo
		err := getJSON(ctx, client, u, nil, &r)
		if ctx.Err() != nil {
			return
		}
		st.Update(func(s *state.Snapshot) {
			if err != nil {
				if s.Weather == nil {
					s.Weather = &state.Weather{Name: w.Name}
				}
				s.Weather.Error = err.Error() // keep last good values on screen
				return
			}
			out := &state.Weather{
				Name: w.Name, Temp: r.Current.Temp, Feels: r.Current.Feels, Code: r.Current.Code,
				Wind: r.Current.Wind, Humidity: r.Current.Humidity, IsDay: r.Current.IsDay == 1,
				Updated: time.Now(),
			}
			for i := range r.Daily.Time {
				out.Daily = append(out.Daily, state.WeatherDay{
					Date: r.Daily.Time[i], Code: at(r.Daily.Code, i), Max: at(r.Daily.Max, i),
					Min: at(r.Daily.Min, i), Pop: at(r.Daily.Pop, i),
				})
			}
			// Next 24 hours starting from the current hour.
			nowKey := time.Now().In(locOrLocal(tz)).Format("2006-01-02T15")
			for i, t := range r.Hourly.Time {
				if len(out.Hourly) >= 24 || len(t) < 13 || t[:13] < nowKey {
					continue
				}
				out.Hourly = append(out.Hourly, state.WeatherHour{Time: t, Temp: at(r.Hourly.Temp, i), Pop: at(r.Hourly.Pop, i)})
			}
			s.Weather = out
		})
	})
}

func at[T any](xs []T, i int) T {
	var zero T
	if i < len(xs) {
		return xs[i]
	}
	return zero
}

func locOrLocal(tz string) *time.Location {
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			return l
		}
	}
	return time.Local
}
