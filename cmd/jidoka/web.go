package main

import "context"

// startWeb starts the web server; its error arrives on the channel.
// Filled in with internal/web.
func (a *app) startWeb(ctx context.Context) <-chan error {
	return make(chan error)
}
