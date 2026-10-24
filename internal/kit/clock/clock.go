// Package clock makes time-dependent control-plane behavior testable.
package clock

import "time"

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type Real struct{}

func (Real) Now() time.Time                         { return time.Now().UTC() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Fake struct {
	C       chan time.Time
	NowTime time.Time
}

func (f Fake) Now() time.Time                       { return f.NowTime }
func (f Fake) After(time.Duration) <-chan time.Time { return f.C }
