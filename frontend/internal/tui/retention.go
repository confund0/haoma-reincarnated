package tui

type retentionLevel struct {
	label   string
	seconds uint32
}

var retentionLevels = []retentionLevel{
	{"Off", 0},
	{"1m", 60},
	{"10m", 600},
	{"1h", 3600},
	{"6h", 6 * 3600},
	{"1d", 24 * 3600},
	{"3d", 3 * 24 * 3600},
	{"1w", 7 * 24 * 3600},
	{"2w", 14 * 24 * 3600},
	{"4w", 28 * 24 * 3600},
}

func retentionOptionIndex(seconds uint32) int {
	for i, lvl := range retentionLevels {
		if lvl.seconds == seconds {
			return i
		}
	}
	return 0
}

func retentionLabel(seconds uint32) string {
	for _, lvl := range retentionLevels {
		if lvl.seconds == seconds {
			return lvl.label
		}
	}
	return "custom"
}

func retentionLabels() []string {
	out := make([]string, len(retentionLevels))
	for i, lvl := range retentionLevels {
		out[i] = "[" + lvl.label + "]"
	}
	return out
}
