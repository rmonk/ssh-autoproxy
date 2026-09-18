package tunnel

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestNextDelayBounds(t *testing.T) {
	base := 2 * time.Second
	max := 2 * time.Minute
	rng := rand.New(rand.NewPCG(1, 2))

	for attempt := 0; attempt < 30; attempt++ {
		raw := base * time.Duration(int64(1)<<uint(min(attempt, 20)))
		if raw > max || raw <= 0 {
			raw = max
		}
		lower := raw / 2
		upper := raw

		delay := nextDelay(attempt, base, max, rng)
		if delay < lower || delay > upper {
			t.Errorf("attempt %d: delay %v out of bounds [%v, %v]", attempt, delay, lower, upper)
		}
		if delay > max {
			t.Errorf("attempt %d: delay %v exceeds max %v", attempt, delay, max)
		}
	}
}

func TestNextDelayNeverZero(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for attempt := 0; attempt < 5; attempt++ {
		if d := nextDelay(attempt, time.Second, time.Minute, rng); d <= 0 {
			t.Errorf("attempt %d: delay must be positive, got %v", attempt, d)
		}
	}
}
