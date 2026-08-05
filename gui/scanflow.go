package gui

import (
	"errors"
	"image"
	"io"
	"log"
	"time"

	"seedhammer.com/gui/assets"
	"seedhammer.com/gui/layout"
	"seedhammer.com/gui/op"
	"seedhammer.com/gui/widget"
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

// scanFlow waits for one NFC payload inside a flow: the start
// screen's scanner, pointed at a single step. accept filters the
// payloads the step takes; any other decodable payload shows reject
// and keeps listening. The back button reports false.
func scanFlow(ctx *Context, th *Colors, title, lead, reject string, accept func(any) bool) (any, bool) {
	scans := make(chan scanResult, 1)
	stop := scanWorker(ctx, scans)
	defer stop()
	backBtn := &Clickable{Button: Button1}
	var status scanStatus
	var rejected bool
	var statusTimeout time.Time
	for !ctx.Done {
		if backBtn.Clicked(ctx) {
			return nil, false
		}
		select {
		case scan := <-scans:
			now := time.Now()
			if now.Before(statusTimeout) {
				status = max(status, scan.Status)
			} else {
				status = scan.Status
				rejected = false
			}
			statusTimeout = now.Add(scanStatusTimeout)
			if obj := scan.Object; obj != nil {
				switch obj.(type) {
				case debugCommand:
					// The provisioning channel has no business inside a
					// flow step; the start screen owns it.
				default:
					if accept(obj) {
						return obj, true
					}
					rejected = true
				}
			}
		default:
		}
		dims := ctx.Platform.DisplaySize()
		r := layout.Rectangle{Max: dims}
		leadOp, lsz := widget.Labelw(&ctx.B, ctx.Styles.lead, dims.X-2*16, th.Text, lead)
		sttxt := ""
		if time.Now().Before(statusTimeout) {
			ctx.WakeupAt(statusTimeout)
			switch {
			case rejected:
				sttxt = reject
			case status == scanFailed:
				sttxt = "Scan error"
			case status == scanOverflow:
				sttxt = "Content too large"
			case status == scanStarted:
				sttxt = "Scanning..."
			case status == scanUnknownFormat:
				sttxt = "Unknown format"
			}
		}
		subt, ssz := widget.Labelw(&ctx.B, ctx.Styles.subtitle, 300, th.Text, sttxt)
		nav, _ := layoutNavigation(&ctx.B, th, dims,
			NavButton{Clickable: backBtn, Style: StyleSecondary, Icon: assets.IconBack},
		)
		titleOp, _ := layoutTitle(ctx, dims.X, th.Text, title)
		ctx.Frame(op.Layer(
			leadOp.Offset(r.Center(lsz)),
			subt.Offset(r.S(ssz).Sub(image.Pt(0, 16))),
			nav,
			titleOp,
			op.Color(&ctx.B, th.Background),
		))
	}
	return nil, false
}
