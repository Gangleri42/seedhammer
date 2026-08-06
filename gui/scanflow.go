package gui

import (
	"errors"
	"io"
	"log"
	"time"
)

// scanWorker runs the NFC scan loop over the platform reader,
// delivering decoded payloads and status changes on scans and waking
// the frame loop for each. The returned stop closes the reader and
// joins the goroutine; callers must run it before returning. Without
// a reader nothing starts and stop is a no-op.
func scanWorker(ctx *Context, scans chan scanResult) (stop func()) {
	r := ctx.Platform.NFCReader()
	if r == nil {
		return func() {}
	}
	closer := make(chan struct{})
	closed := make(chan struct{})
	wakeup := ctx.Platform.Wakeup
	go func() {
		s := new(scanner)
		var lastStatus scanStatus
		var lastWake time.Time
		for {
			select {
			case <-closer:
				close(closed)
				return
			default:
			}
			obj, err := s.Scan(r)
			scan := scanResult{
				Object: obj,
			}
			switch {
			case errors.Is(err, errScanInProgress):
				scan.Status = scanStarted
			case errors.Is(err, errScanUnknownFormat):
				scan.Status = scanUnknownFormat
			case err == nil || err == io.EOF:
			default:
				scan.Status = scanFailed
				log.Printf("nfc scan: %v", err)
			}
			// Deliver only news: every wakeup redraws a full frame,
			// and a redraw per received chunk starves this goroutine
			// past the writer's frame waiting time. Unchanged status
			// is refreshed at half the label decay interval.
			if scan.Object == nil && scan.Status == lastStatus &&
				time.Since(lastWake) < scanStatusTimeout/2 {
				continue
			}
			// Merge the previous result.
			select {
			case old := <-scans:
				if scan.Object == nil {
					scan.Object = old.Object
				}
				scan.Status = max(scan.Status, old.Status)
			default:
			}
			scans <- scan
			wakeup()
			lastStatus = scan.Status
			lastWake = time.Now()
			if scan.Status == scanFailed {
				// Wait a bit before attempting to scan again, but let a
				// stop cut the wait short: the reader's own close error
				// lands here, and the flow's cleanup joins this loop.
				select {
				case <-closer:
				case <-time.After(1 * time.Second):
				}
			}
		}
	}()
	return func() {
		close(closer)
		r.Close()
		<-closed
	}
}

// scanStatusText is the label a scan status shows while it decays.
func scanStatusText(status scanStatus) string {
	switch status {
	case scanFailed:
		return "Scan error"
	case scanOverflow:
		return "Content too large"
	case scanStarted:
		return "Scanning..."
	case scanUnknownFormat:
		return "Unknown format"
	}
	return ""
}
