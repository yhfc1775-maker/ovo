package handler

import (
	"context"
	"net"
	"time"
)

var taskLookupIP = net.DefaultResolver.LookupIP

// Admission checks without a task context still bound each lookup. Dial and
// probe paths pass the existing task context so DNS shares its cancellation.
func lookupIPWithContext(parent context.Context, host string) ([]net.IP, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	return taskLookupIP(ctx, "ip", host)
}

func lookupIPWithBudget(host string) ([]net.IP, error) {
	return lookupIPWithContext(context.Background(), host)
}
