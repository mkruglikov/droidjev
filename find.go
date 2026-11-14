// Deterministic label search in scrollable content: snapshot → match →
// (tap | scroll) → repeat. No model calls — a long alphabetized list is a code
// problem, not a judgment problem.
//
// Ported from the Node implementation: ranking, the hash-stall
// wake-and-retry-once, the 60% scroll, the 600ms settle, and both stop reasons
// are kept exact. One deliberate divergence: the Node loop only scrolled down,
// so a label above the starting position was unreachable; this scan scrolls to
// the top of the list first (an up-scroll that changes nothing twice), then
// walks down — end_of_list is now an exhaustive verdict.

package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

const findMaxIterations = 20

// FindOpts are findLabel's arguments. MaxIterations <= 0 means the 20-step
// default; Log <= nil is a no-op.
type FindOpts struct {
	Serial        string
	Label         string
	TapIt         bool
	MaxIterations int
	Log           func(string)
}

// FindResult is the findLabel outcome. Match and Center are empty/nil when
// nothing matched; Reason is "" on a hit and end_of_list or max_iterations
// otherwise.
type FindResult struct {
	Found      bool
	Iterations int
	Match      string
	Center     *Point
	Matches    []string
	Tapped     bool
	Reason     string
}

var (
	quotedLabelRe = regexp.MustCompile(`"([^"]*)"`)
	// Internal containment metadata (`holds [13,14,15]`) is not searchable
	// text: a query like "14" must not hit an index list.
	holdsSuffixRe = regexp.MustCompile(` holds \[[\d,]*\]$`)
)

// MatchRows ranks the rows matching query: exact quoted label beats substring,
// clickable beats not, topmost (smallest index) wins ties.
func MatchRows(t *Table, query string) ([]*Row, error) {
	q := strings.ToLower(JSTrim(query))
	if q == "" {
		return nil, NewDroidJevError("find needs a non-empty label")
	}
	var exact, partial []*Row
	if t != nil {
		for _, r := range t.Rows {
			if r == nil {
				continue
			}
			label := ""
			if m := quotedLabelRe.FindStringSubmatch(r.Row); m != nil {
				label = strings.ToLower(m[1])
			}
			searchable := strings.ToLower(holdsSuffixRe.ReplaceAllString(r.Row, ""))
			if label == q {
				exact = append(exact, r)
			} else if strings.Contains(searchable, q) {
				partial = append(partial, r)
			}
		}
	}
	pool := exact
	if len(pool) == 0 {
		pool = partial
	}
	if len(pool) == 0 {
		return []*Row{}, nil
	}
	sort.SliceStable(pool, func(i, j int) bool {
		ci, cj := rowClickable(pool[i]), rowClickable(pool[j])
		if ci != cj {
			return ci
		}
		return pool[i].Idx < pool[j].Idx
	})
	return pool, nil
}

func rowClickable(r *Row) bool { return r != nil && r.El != nil && r.El.Clickable }

// findIO is FindLabel's device seam: production is the real device; tests
// drive a scripted list. Scroll swipes AND waits out the fling (the settle is
// part of one scroll step for the hash-stall logic).
type findIO struct {
	Snapshot func(serial string) (*Snapshot, error)
	Scroll   func(serial string, s *Snapshot, dir string) error
	Wake     func(serial string) error
	Tap      func(serial string, el *Element) error
}

// FindLabel scrolls the screen looking for label, optionally tapping the best
// match. The scan runs in two phases: it first scrolls UP until the screen
// stops changing (the top of the list — a label can sit above the position
// the run starts from), then scans DOWN through the whole list. The Node
// implementation only scrolled down.
func FindLabel(opts FindOpts) (FindResult, error) {
	return findScan(opts, findIO{
		Snapshot: TakeSnapshot,
		Scroll: func(serial string, s *Snapshot, dir string) error {
			screenW, screenH := defaultScreenW, defaultScreenH
			if s.Screen != nil {
				screenW, screenH = s.Screen.W, s.Screen.H
			}
			anchor := ScrollAnchor(s.Table, Screen{W: screenW, H: screenH})
			// Math.round(screen.h * 0.6): halves round up.
			dy := int(math.Floor(float64(screenH)*0.6 + 0.5))
			if err := ScrollSwipe(serial, anchor, dy, screenH, dir, 0); err != nil {
				return err
			}
			// Outlast the fling's overscroll bounce: while it plays, rows
			// flicker across the clip edge and every dump hashes differently,
			// faking progress. Polling ends as soon as the hierarchy stops
			// moving.
			_, err := SettleLayout(serial, 600)
			return err
		},
		Wake: WakeAndUnlock,
		Tap:  func(serial string, el *Element) error { return Tap(serial, centerOf(el)) },
	})
}

// findScan is the two-phase search loop behind the findIO seam.
func findScan(opts FindOpts, io findIO) (FindResult, error) {
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	maxIterations := opts.MaxIterations
	if maxIterations <= 0 {
		maxIterations = findMaxIterations
	}
	prevHash := ""
	hasPrev := false
	stalled := false // one hash-repeat triggers a wake-and-retry (doze eats gestures silently)
	up := true       // phase 1: scroll to the top; phase 2: scan down
	for i := 1; i <= maxIterations; i++ {
		s, err := io.Snapshot(opts.Serial)
		if err != nil {
			return FindResult{}, err
		}
		matches, err := MatchRows(s.Table, opts.Label)
		if err != nil {
			return FindResult{}, err
		}
		if len(matches) > 0 {
			best := matches[0]
			// A coordinate tap on the label hits whatever clickable element
			// contains it, so tapping a text row works for standard list rows.
			if opts.TapIt {
				if err := io.Tap(opts.Serial, best.El); err != nil {
					return FindResult{}, err
				}
				log(fmt.Sprintf("iteration %d: tapped %s", i, best.Row))
			} else {
				log(fmt.Sprintf("iteration %d: found %s", i, best.Row))
			}
			return FindResult{
				Found:      true,
				Iterations: i,
				Match:      best.Row,
				Center:     matchCenter(best.El),
				Matches:    rowTextsOf(matches),
				Tapped:     opts.TapIt,
			}, nil
		}
		// No scrollable container on screen: scrolling cannot reveal anything,
		// so stop instead of swiping into overscroll until maxIterations.
		if !tableHasScrollable(s.Table) {
			return findMiss(i, "end_of_list"), nil
		}
		h := TableHash(s.Table)
		if hasPrev && h == prevHash {
			if stalled {
				if !up {
					return findMiss(i, "end_of_list"), nil
				}
				// Scrolling up changed nothing twice: this is the top. Switch
				// to the down-scan and keep this iteration's scroll.
				up = false
				stalled = false
				log(fmt.Sprintf("iteration %d: reached the top — scanning down", i))
			} else {
				stalled = true
				log(fmt.Sprintf("iteration %d: screen unchanged — waking display and retrying once", i))
				if err := io.Wake(opts.Serial); err != nil {
					return FindResult{}, err
				}
			}
		} else {
			stalled = false
		}
		prevHash, hasPrev = h, true
		dir := "down"
		if up {
			dir = "up"
		}
		if err := io.Scroll(opts.Serial, s, dir); err != nil {
			return FindResult{}, err
		}
		log(fmt.Sprintf("iteration %d: \"%s\" not visible — scrolled %s", i, opts.Label, dir))
	}
	return findMiss(maxIterations, "max_iterations"), nil
}

func findMiss(iterations int, reason string) FindResult {
	return FindResult{Iterations: iterations, Matches: []string{}, Reason: reason}
}

func tableHasScrollable(t *Table) bool {
	if t == nil {
		return false
	}
	for _, r := range t.Rows {
		if r != nil && r.El != nil && r.El.Scrollable {
			return true
		}
	}
	return false
}

func rowTextsOf(rows []*Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Row
	}
	return out
}

// matchCenter is `best.el.center`; a host-supplied row may lack it, so fall
// back to the bounds' rounded center.
func matchCenter(el *Element) *Point {
	if el == nil {
		return nil
	}
	if el.Center != nil {
		return el.Center
	}
	if el.Bounds != nil {
		p := centerOf(el)
		return &p
	}
	return nil
}
