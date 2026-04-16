package review

import "fmt"

// formatDurationMs formats a duration in a human-readable way (e.g., "3m42s", "1h22m").
func formatDurationMs(ms int64) string {
	sec := ms / 1000
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	min := sec / 60
	sec = sec % 60
	if min < 60 {
		return fmt.Sprintf("%dm%02ds", min, sec)
	}
	hr := min / 60
	min = min % 60
	return fmt.Sprintf("%dh%02dm", hr, min)
}
