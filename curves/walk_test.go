package curves

import (
	"errors"
	"reflect"
	"testing"

	"seedhammer.com/engrave"
)

// Walk must fill the same stats Parse fills and stream the same
// commands Engraving replays, dictionary payloads included.
func TestWalkMatchesParse(t *testing.T) {
	dict, err := EncodeGroups(10, 3, sampleGroups())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		prm  engrave.Params
	}{
		{"plain", payload("M 0 0 L 800 800 C 1200 1200 1600 800 2000 800"), params},
		{"dict", dict, engrave.SH2Params},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pd, err := Parse(tc.data, tc.prm)
			if err != nil {
				t.Fatal(err)
			}
			wd, err := Open(tc.data, tc.prm)
			if err != nil {
				t.Fatal(err)
			}
			var walked []engrave.Command
			calls, lastDone, total := 0, -1, -1
			if err := wd.Walk(func(done, tot int) bool {
				calls++
				if done < lastDone {
					t.Fatalf("progress went backwards: %d after %d", done, lastDone)
				}
				if total >= 0 && tot != total {
					t.Fatalf("total changed: %d then %d", total, tot)
				}
				lastDone, total = done, tot
				return true
			}, func(cmd engrave.Command) bool {
				walked = append(walked, cmd)
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if calls == 0 || lastDone > total {
				t.Fatalf("progress: %d calls, final %d/%d", calls, lastDone, total)
			}
			if pd.Strokes != wd.Strokes || pd.Knots != wd.Knots ||
				pd.MaxStrokeKnots != wd.MaxStrokeKnots || pd.Bounds != wd.Bounds {
				t.Errorf("stats differ: Parse{%d %d %d %v} Walk{%d %d %d %v}",
					pd.Strokes, pd.Knots, pd.MaxStrokeKnots, pd.Bounds,
					wd.Strokes, wd.Knots, wd.MaxStrokeKnots, wd.Bounds)
			}
			if replay := collect(pd.Engraving()); !reflect.DeepEqual(walked, replay) {
				t.Fatalf("walked %d commands, replay %d; streams differ", len(walked), len(replay))
			}
		})
	}
}

func TestWalkCancel(t *testing.T) {
	d, err := Open(payload("M 0 0 L 800 800 L 1200 400 L 1600 800"), params)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = d.Walk(func(done, total int) bool {
		calls++
		return calls < 2
	}, nil)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("cancelled walk: %v, want ErrCanceled", err)
	}
	if calls != 2 {
		t.Fatalf("progress called %d times, want 2", calls)
	}
	// A canceled drawing walks fully on retry.
	if err := d.Walk(nil, nil); err != nil {
		t.Fatal(err)
	}
	if d.Strokes == 0 {
		t.Error("retry after cancel left no stats")
	}
}
