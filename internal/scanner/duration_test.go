package scanner

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-5 * time.Second, "0ms"},
		{0, "0ms"},
		{850 * time.Millisecond, "850ms"},
		{1 * time.Second, "1秒"},
		{3200 * time.Millisecond, "3.2秒"},
		{9940 * time.Millisecond, "9.9秒"},
		{10 * time.Second, "10秒"},
		{45 * time.Second, "45秒"},
		{60 * time.Second, "1分"},
		{192 * time.Second, "3分12秒"},
		{3600 * time.Second, "1小时"},
		{3900 * time.Second, "1小时5分"},
		{4523 * time.Second, "1小时15分23秒"},
		{7205 * time.Second, "2小时0分5秒"},
	}
	for _, tc := range cases {
		if got := FormatDuration(tc.in); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
