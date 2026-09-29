package ratelimit

import (
	"testing"
	"testing/synctest"
	"time"
)

func TestSignInLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := New()
		now := time.Now()
		if d := g.Allow(now, "ada", "1.1.1.1"); d.Paused || d.Delay != 0 {
			t.Fatal(d)
		}
		g.Fail(now, "ada", "1.1.1.1")
		d := g.Allow(now, "ada", "1.1.1.1")
		if d.Delay != time.Second {
			t.Fatal(d.Delay)
		}
		for i := 0; i < 4; i++ {
			g.Fail(now, "ada", "1.1.1.1")
		}
		d = g.Allow(now, "ada", "1.1.1.1")
		if !d.Paused {
			t.Fatal("pair should pause after 5")
		}
		time.Sleep(15 * time.Minute)
		if g.Allow(time.Now(), "ada", "1.1.1.1").Paused {
			t.Fatal("pause expired")
		}
		g.Reset("ada")
		if g.Allow(time.Now(), "ada", "1.1.1.1").Delay != 0 {
			t.Fatal("delay stuck")
		}
	})
}

func TestIPPauseAndServiceCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := New()
		now := time.Now()
		for i := 0; i < 20; i++ {
			g.Fail(now, "u", "9.9.9.9")
		}
		if !g.Allow(now, "other", "9.9.9.9").Paused {
			t.Fatal("ip pause")
		}
		for i := 0; i < serviceMax; i++ {
			if !g.AllowService(now, "ada") {
				t.Fatal(i)
			}
			g.ServiceFail(now, "ada")
		}
		if g.AllowService(now, "ada") {
			t.Fatal("service cap")
		}
		time.Sleep(serviceWin)
		if !g.AllowService(time.Now(), "ada") {
			t.Fatal("hour passed")
		}
		for i := 0; i < signupMax; i++ {
			if !g.AllowSignup(now, "1.2.3.4") {
				t.Fatal(i)
			}
			g.Signup(now, "1.2.3.4")
		}
		if g.AllowSignup(now, "1.2.3.4") {
			t.Fatal("signup cap")
		}
	})
}
