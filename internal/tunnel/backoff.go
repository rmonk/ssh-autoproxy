package tunnel

import (
	"math/rand/v2"
	"time"
)

// nextDelay computes the reconnect delay for the given 0-based attempt
// count, using capped exponential backoff with equal jitter: half the raw
// delay is fixed, half is randomized, so the delay is never zero and
// simultaneous reconnects (e.g. after a Wi-Fi blip) get spread out.
func nextDelay(attempt int, base, max time.Duration, rng *rand.Rand) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = base
	}
	shift := attempt
	if shift > 20 {
		shift = 20
	}
	raw := base * time.Duration(int64(1)<<uint(shift))
	if raw > max || raw <= 0 {
		raw = max
	}
	half := raw / 2
	if half <= 0 {
		return raw
	}
	return half + time.Duration(rng.Int64N(int64(half)+1))
}
