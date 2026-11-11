//go:build release

// Package demo is empty in a release build: the demo mode is not part of
// what is shipped.
package demo

import (
	"errors"
	"time"
)

// ErrUnavailable is all a release build says about it.
var ErrUnavailable = errors.New("not available")

// Prepare does nothing in a release build.
func Prepare(string, string, time.Time) (string, error) {
	return "", ErrUnavailable
}
