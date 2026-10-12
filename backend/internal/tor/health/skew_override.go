package health

import (
	"log/slog"
	"os"
	"strconv"
	"sync"
)

const skewEnv = "HAOMA_TOR_CLOCK_SKEW_OFFSET"

var (
	skewOnce sync.Once
	skewSecs int
)

func skewOffset() int {
	skewOnce.Do(func() {
		raw := os.Getenv(skewEnv)
		if raw == "" {
			return
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			slog.Warn("tor clock skew offset ignored (not a whole number of seconds)",
				slog.String("env", skewEnv),
				slog.String("value", raw),
			)
			return
		}
		skewSecs = v
		slog.Info("tor clock skew reading is shifted",
			slog.Int("offset_seconds", v),
		)
	})
	return skewSecs
}
