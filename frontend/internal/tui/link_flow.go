package tui

import (
	"strings"
	"sync"
	"time"
)

type linkFlowMode int

const (
	flowIdle linkFlowMode = iota
	flowOffer
	flowJoin

	flowGuardOnly
)

type logLine struct {
	text    string
	outcome string

	aside bool

	torSlow bool
}

const (
	outcomeOK      = "OK"
	outcomeWarning = "WARNING"
	outcomeFailed  = "FAILED"
)

const logLeaderWidth = 38

type linkFlowPhase int

const (
	phaseCollect linkFlowPhase = iota
	phaseRun
	phaseDone
)

type linkFlow struct {
	mu sync.Mutex

	mode  linkFlowMode
	phase linkFlowPhase

	ownPIN   string
	otherPIN string
	words    string

	handleID string

	lines []logLine

	retryable bool

	note string

	running bool

	failed bool

	dialSince time.Time

	linked bool

	now func() time.Time

	frame int
}

func (f *linkFlow) snapshot() (linkFlowMode, linkFlowPhase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode, f.phase
}

func (f *linkFlow) Say(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, logLine{text: line})
	f.running = true
}

func (f *linkFlow) Aside(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = append(f.lines, logLine{text: line, aside: true})
}

func (f *linkFlow) Step(done, next string) {
	f.mu.Lock()
	if len(f.lines) > 0 && done != "" {
		f.lines[len(f.lines)-1].outcome = done
	}
	if next != "" {
		f.lines = append(f.lines, logLine{text: next})
	}
	f.running = next != ""
	f.mu.Unlock()
}

func (f *linkFlow) Fail(reason string) {
	f.mu.Lock()
	if len(f.lines) > 0 {
		f.lines[len(f.lines)-1].outcome = outcomeFailed
	}
	f.lines = append(f.lines, logLine{text: reason, aside: true})
	f.running = false
	f.failed = true
	f.phase = phaseDone
	f.dialSince = time.Time{}
	f.mu.Unlock()
}

func (f *linkFlow) FailRetryable(reason string) {
	f.Fail(reason)
	f.mu.Lock()
	f.retryable = true
	f.mu.Unlock()
}

func (f *linkFlow) Retryable() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retryable
}

func (f *linkFlow) Resume(next string) {
	f.mu.Lock()
	f.failed = false
	f.retryable = false
	f.running = true
	f.phase = phaseRun
	f.lines = append(f.lines, logLine{text: next})
	f.mu.Unlock()
}

func (f *linkFlow) Finish(last string) {
	f.mu.Lock()
	if len(f.lines) > 0 {
		f.lines[len(f.lines)-1].outcome = outcomeOK
	}
	if last != "" {
		f.lines = append(f.lines, logLine{text: last, outcome: outcomeOK})
	}
	f.running = false
	f.phase = phaseDone
	f.dialSince = time.Time{}
	f.mu.Unlock()
}

func (f *linkFlow) FinishLinked(last string) {
	f.mu.Lock()
	f.lines = append(f.lines, logLine{text: last, outcome: outcomeOK})
	f.running = false
	f.failed = false
	f.retryable = false
	f.phase = phaseDone
	f.dialSince = time.Time{}
	f.mu.Unlock()
}

func (f *linkFlow) FinishLinkedWarning(reason, last string) {
	f.mu.Lock()
	if len(f.lines) > 0 {
		f.lines[len(f.lines)-1].outcome = outcomeWarning
	}
	f.lines = append(f.lines, logLine{text: reason, aside: true}, logLine{text: last})
	f.running = false
	f.failed = false
	f.retryable = false
	f.phase = phaseDone
	f.dialSince = time.Time{}
	f.mu.Unlock()
}

func (f *linkFlow) StartDialWait() {
	f.mu.Lock()
	f.dialSince = f.clock()
	f.mu.Unlock()
}

func (f *linkFlow) EndDialWait() {
	f.mu.Lock()
	f.dialSince = time.Time{}
	f.mu.Unlock()
}

func (f *linkFlow) RetryLater(reason string, torSlow bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mode != flowJoin || f.phase != phaseRun || f.linked {
		return
	}
	if len(f.lines) > 0 {
		f.lines[len(f.lines)-1].outcome = outcomeFailed
	}
	f.lines = append(f.lines, logLine{text: reason, aside: true, torSlow: torSlow})
	f.running = false
	f.failed = false
	f.retryable = false
	f.phase = phaseCollect
	f.dialSince = time.Time{}
}

func (f *linkFlow) MarkLinked() {
	f.mu.Lock()
	f.linked = true
	f.mu.Unlock()
}

func (f *linkFlow) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *linkFlow) SetWords(handleID, words string) {
	f.mu.Lock()
	f.handleID = handleID
	f.words = words
	f.mu.Unlock()
}

func (f *linkFlow) Handle() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handleID
}

func (f *linkFlow) PINs() (own, other string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownPIN, f.otherPIN
}

func (f *linkFlow) Words() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.words
}

func (f *linkFlow) Failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failed
}

func (f *linkFlow) Tick() {
	f.mu.Lock()
	f.frame++
	f.mu.Unlock()
}

type linkFlowState struct {
	mode      linkFlowMode
	phase     linkFlowPhase
	retryable bool
	handle    string
	ownPIN    string
	otherPIN  string
	words     string
	log       string
}

func (f *linkFlow) state() linkFlowState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return linkFlowState{
		mode:      f.mode,
		phase:     f.phase,
		retryable: f.retryable,
		handle:    f.handleID,
		ownPIN:    f.ownPIN,
		otherPIN:  f.otherPIN,
		words:     f.words,
		log:       f.renderLog(),
	}
}

func (f *linkFlow) Log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renderLog()
}

func (f *linkFlow) renderLog() string {

	out := []string{StyleLinkLogLabel + " Action log:" + StyleLinkBody, ""}

	for i, ln := range f.lines {
		if ln.aside {
			style := StyleLinkDim
			if ln.torSlow {
				style = StyleTorSlow
			}
			out = append(out, "   "+style+ln.text+StyleLinkBody)
			continue
		}
		marker := ""
		if f.running && i == len(f.lines)-1 {
			marker = StyleLinkLogWait +
				string(spinnerFrames[f.frame%len(spinnerFrames)]) + StyleLinkBody
		} else if ln.outcome != "" {
			style := StyleLinkOK
			switch ln.outcome {
			case outcomeFailed:
				style = StyleLinkBad
			case outcomeWarning:
				style = StyleLinkStress
			}
			marker = style + ln.outcome + StyleLinkBody
		}
		out = append(out, "   "+StyleLinkLogLine+leaderFor(ln.text, marker != "")+StyleLinkBody+marker)
	}

	if f.running && !f.dialSince.IsZero() {
		out = append(out, "   "+StyleLinkDim+joinWaitLinkLine(f.clock().Sub(f.dialSince))+StyleLinkBody)
	}

	if f.note != "" {
		out = append(out, "", "   "+StyleLinkBad+f.note+StyleLinkBody)
	}
	return strings.Join(out, "\n")
}

func leaderFor(text string, wantsOutcome bool) string {
	if !wantsOutcome {
		return text
	}
	n := logLeaderWidth - len([]rune(text))
	if n < 1 {
		return text + " "
	}
	return text + " " + strings.Repeat(".", n) + " "
}

func (f *linkFlow) Note(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note = line
}

func (f *linkFlow) ClearNote() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note = ""
}

func (f *linkFlow) StepOnce(done, next string) {
	f.mu.Lock()
	if len(f.lines) > 0 && f.lines[len(f.lines)-1].text == next {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	f.Step(done, next)
}
