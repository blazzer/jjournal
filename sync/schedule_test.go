package sync

import (
	"testing"
	"time"
)

func TestFeedIntervalBounds(t *testing.T) {
	if JitteredInterval(FeedInterval, FeedJitter, 0) != 50*time.Minute {
		t.Fatal("low")
	}
	if JitteredInterval(FeedInterval, FeedJitter, 1) != 70*time.Minute {
		t.Fatal("high")
	}
	for i := 0; i < 20; i++ {
		d := JitteredInterval(FeedInterval, FeedJitter, Unit())
		if d < 50*time.Minute || d > 70*time.Minute {
			t.Fatal(d)
		}
	}
}
