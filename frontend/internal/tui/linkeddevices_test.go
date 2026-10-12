package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

func TestDestroyWaitRemaining_RoundsUpAndNeverReadsZeroWhileRunning(t *testing.T) {
	cases := []struct {
		name          string
		deadline, now int64
		want          string
	}{
		{"forty seconds left", 40, 0, "00:01"},
		{"one second left", 1, 0, "00:01"},
		{"the full window", 6 * 3600, 0, "06:00"},
		{"an hour and a half", 5400, 0, "01:30"},
		{"exactly at the deadline", 10, 10, "00:00"},
		{"past the deadline", 10, 99, "00:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := destroyWaitRemaining(c.deadline, c.now); got != c.want {
				t.Errorf("destroyWaitRemaining(%d, %d) = %q, want %q", c.deadline, c.now, got, c.want)
			}
		})
	}
}

func TestLinkedDeviceIntro_APendingDestroyTakesOverTheBox(t *testing.T) {
	st := linkedDevicesState{
		devicesKnown:    true,
		destroyPINKnown: true,
		destroyPIN: ipc.DestroyPINListedResponse{
			Waiting: &ipc.DestroyWaiting{
				PeerID:   "5b2e94a07c1d43f8ae60b3d519f78c24",
				Deadline: time.Now().Unix() + 5400,
			},
		},
	}
	got := linkedDeviceIntro(st, nil)
	if !strings.Contains(got, "01:30") {
		t.Errorf("the intro does not say how long is left:\n%s", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("the intro must not imply the far device was erased:\n%s", got)
	}
	if !strings.Contains(got, "does not undo") {
		t.Errorf("the intro must say stopping undoes nothing:\n%s", got)
	}
	if strings.Contains(got, "no linked device") {
		t.Errorf("the pending destroy was overwritten by the empty-state text:\n%s", got)
	}
}

func TestLinkedDevicesShape_DecidesEveryButtonSet(t *testing.T) {
	linked := []ipc.DeviceLinkEntry{{PeerID: "peer-sibling"}}
	waiting := ipc.DestroyPINListedResponse{
		Waiting: &ipc.DestroyWaiting{PeerID: "peer-gone", Deadline: time.Now().Unix() + 600},
	}

	cases := []struct {
		name string
		st   linkedDevicesState
		flow *linkFlow

		want string

		buttons []string
		fields  []string
	}{{
		name:    "a destroy still waiting offers only the way out of it",
		st:      linkedDevicesState{devices: linked, destroyPIN: waiting, destroyPINKnown: true},
		want:    "waiting",
		fields:  []string{riskLabel},
		buttons: []string{"Stop waiting"},
	}, {
		name: "linked and this device has no guard",
		st:   linkedDevicesState{devices: linked, destroyPINKnown: true},
		want: "linked-without-guard:peer-sibling",

		buttons: []string{"Set destroy PIN"},
	}, {
		name:    "linked with a guard set",
		st:      linkedDevicesState{devices: linked, destroyPINKnown: true, destroyPIN: ipc.DestroyPINListedResponse{Set: true}},
		want:    "linked:peer-sibling",
		fields:  []string{riskLabel},
		buttons: []string{"Verify destroy PIN…", "Unlink", "Destroy…"},
	}, {
		name:    "linked, and the guard answer has not arrived",
		st:      linkedDevicesState{devices: linked},
		want:    "linked:peer-sibling",
		fields:  []string{riskLabel},
		buttons: []string{"Verify destroy PIN…", "Unlink", "Destroy…"},
	}, {
		name:    "holds contacts, so it cannot join a link",
		st:      linkedDevicesState{devicesKnown: true, peerIDs: []string{"peer-alice"}},
		want:    "holds-contacts",
		buttons: []string{"Link a new device"},
	}, {
		name:    "empty, so it can do either",
		st:      linkedDevicesState{devicesKnown: true},
		want:    "empty",
		buttons: []string{"Link a new device", "Join a link invitation"},
	}, {
		name:    "collecting for an offer",
		flow:    &linkFlow{mode: flowOffer, phase: phaseCollect},
		want:    fmt.Sprintf("run:%v:%v:false:false", flowOffer, phaseCollect),
		buttons: []string{"Proceed", "Cancel"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}, {
		name:    "collecting for a join",
		flow:    &linkFlow{mode: flowJoin, phase: phaseCollect},
		want:    fmt.Sprintf("run:%v:%v:false:false", flowJoin, phaseCollect),
		buttons: []string{"Proceed", "Cancel"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}, {
		name: "collecting for a guard-only run, which has no words row",
		flow: &linkFlow{mode: flowGuardOnly, phase: phaseCollect},
		want: fmt.Sprintf("run:%v:%v:false:false", flowGuardOnly, phaseCollect),

		buttons: []string{"Proceed", "Cancel"},
		fields:  []string{labelOwnPIN, labelOtherPIN},
	}, {
		name:    "running, with an invitation standing",
		flow:    &linkFlow{mode: flowOffer, phase: phaseRun, handleID: "handle-1"},
		want:    fmt.Sprintf("run:%v:%v:false:true", flowOffer, phaseRun),
		buttons: []string{"Cancel this invitation"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}, {
		name:    "running, with nothing published yet",
		flow:    &linkFlow{mode: flowOffer, phase: phaseRun},
		want:    fmt.Sprintf("run:%v:%v:false:false", flowOffer, phaseRun),
		buttons: []string{"Cancel"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}, {
		name:    "finished, and this device can still set its guard",
		flow:    &linkFlow{mode: flowOffer, phase: phaseDone, retryable: true},
		want:    fmt.Sprintf("run:%v:%v:true:false", flowOffer, phaseDone),
		buttons: []string{"Set destroy PIN", "Close"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}, {
		name:    "finished, with nothing left to retry",
		flow:    &linkFlow{mode: flowOffer, phase: phaseDone},
		want:    fmt.Sprintf("run:%v:%v:false:false", flowOffer, phaseDone),
		buttons: []string{"Close"},
		fields:  []string{labelOwnPIN, labelOtherPIN, labelWords},
	}}

	type widgetSet struct{ fields, buttons []string }
	byShape := map[string]widgetSet{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fs *linkFlowState
			if tc.flow != nil {
				s := tc.flow.state()
				fs = &s
			}
			if got := linkedDevicesShape(tc.st, fs); got != tc.want {
				t.Fatalf("shape = %q, want %q", got, tc.want)
			}

			sp := &settingsPage{linked: tc.st, dirty: map[string]bool{}}
			buttons := tview.NewForm()

			items := buttons
			if fs == nil {
				buildLinkIdleButtons(&App{}, sp, buttons)
			} else {
				fields := tview.NewForm()
				buildLinkFlowRows(&App{}, sp, tc.flow, fs,
					fields, buttons, tview.NewTextView(), tview.NewTextView())
				items = fields
			}
			if got := formFieldLabels(items); !equalStrings(got, tc.fields) {
				t.Errorf("form items = %v, want %v", got, tc.fields)
			}
			if got := formButtonLabels(buttons); !equalStrings(got, tc.buttons) {
				t.Errorf("buttons = %v, want %v", got, tc.buttons)
			}

			if seen, ok := byShape[tc.want]; ok {
				if !equalStrings(seen.fields, tc.fields) || !equalStrings(seen.buttons, tc.buttons) {
					t.Fatalf("shape %q draws items %v and buttons %v here, "+
						"items %v and buttons %v elsewhere — "+
						"a branch of the builder has no branch in linkedDevicesShape, "+
						"so a daemon answer that moves between the two never rebuilds the page",
						tc.want, tc.fields, tc.buttons, seen.fields, seen.buttons)
				}
				return
			}
			byShape[tc.want] = widgetSet{fields: tc.fields, buttons: tc.buttons}
		})
	}
}

func formButtonLabels(f *tview.Form) []string {
	out := []string{}
	for i := 0; i < f.GetButtonCount(); i++ {
		out = append(out, f.GetButton(i).GetLabel())
	}
	return out
}

func formFieldLabels(f *tview.Form) []string {
	out := []string{}
	for i := 0; i < f.GetFormItemCount(); i++ {
		out = append(out, strings.TrimSpace(f.GetFormItem(i).GetLabel()))
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}
