package main

import (
	"context"
	"sync"
	"time"
)

// Job represents a single scan task: one IP + one port to probe.
type Job struct {
	IP   string
	Port int
}

// Result holds the outcome of a single port scan.
type Result struct {
	IP   string
	Port int
	Open bool
}

// Worker pulls scan jobs from the jobs channel and pushes results.
//
// Worker pool pattern:
//   - N workers share a single jobs channel (fan-out).
//   - When a job is available, exactly one worker picks it up.
//   - Results go into a shared results channel (fan-in).
//   - Closing the jobs channel signals all workers to stop.
//
// Each worker respects context cancellation — if the user sends SIGINT,
// in-progress scans are abandoned and the worker exits.
func Worker(ctx context.Context, jobs <-chan Job, results chan<- Result, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()

	for job := range jobs {
		// Check for cancellation before starting each scan.
		// This avoids starting new work when we're shutting down.
		select {
		case <-ctx.Done():
			return
		default:
		}

		open := ScanPort(ctx, job.IP, job.Port, timeout)
		results <- Result{
			IP:   job.IP,
			Port: job.Port,
			Open: open,
		}
	}
}
