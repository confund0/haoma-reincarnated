package tui

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rivo/tview"
)

const (
	testWipeBoxRows     = wipeBlockRows
	testWipeChromeRows  = 3
	testWipeNoteMinRows = 2
	testWipeBoxCols     = 72
	testWipeChromeCols  = 4
)

func TestWipeBlock_TheRowsAddUpAtTheFloor(t *testing.T) {
	body := wipeBodyRows(wipeBodyFloorWidth)
	used := body + wipeFormRows + testWipeNoteMinRows + testWipeChromeRows
	if used > testWipeBoxRows {
		t.Fatalf("the block needs %d rows and the Advanced page gives it %d "+
			"(body %d + form %d + note %d + chrome %d). Grow the literal in "+
			"buildAdvancedForm or take a line out of the body.",
			used, testWipeBoxRows, body, wipeFormRows, testWipeNoteMinRows, testWipeChromeRows)
	}
}

func TestWipeBlock_TheFloorWidthIsTheBoxWidth(t *testing.T) {
	if got := testWipeBoxCols - testWipeChromeCols; got != wipeBodyFloorWidth {
		t.Fatalf("the body is sized at %d columns and the box has %d",
			wipeBodyFloorWidth, got)
	}
}

func TestWipeBlock_TheFormRowsAreTheFormsOwn(t *testing.T) {
	a := &App{}
	sp := &settingsPage{}
	flex, ok := buildWipeDeviceBlock(a, sp).(*tview.Flex)
	if !ok {
		t.Fatal("the block is no longer a Flex")
	}
	flex.SetRect(0, 0, testWipeBoxCols, testWipeBoxRows)
	if _, _, w, _ := flex.GetInnerRect(); w != wipeBodyFloorWidth {
		t.Fatalf("the box's inner width is %d, not the %d the body is sized at",
			w, wipeBodyFloorWidth)
	}

	if n := flex.GetItemCount(); n != 3 {
		t.Fatalf("the block holds %d items, not the body, the form and the note", n)
	}
	form, ok := flex.GetItem(1).(*tview.Form)
	if !ok {
		t.Fatal("the middle item is no longer the form")
	}

	form.SetRect(0, 0, wipeBodyFloorWidth, wipeFormRows)
	if _, y, _, h := form.GetInnerRect(); y != 0 || h != wipeFormRows {
		t.Fatalf("the form draws into rows %d..%d of the %d it was given; "+
			"the rows above and below belong to the box and the note",
			y, y+h-1, wipeFormRows)
	}

	want := form.GetFormItemCount() + form.GetButtonCount() +
		(form.GetFormItemCount() + form.GetButtonCount() - 1)
	if wipeFormRows != want {
		t.Fatalf("the form is given %d rows and needs %d", wipeFormRows, want)
	}
}

func TestWipeBlock_EveryBodySentenceIsInTheBody(t *testing.T) {
	text := wipeBodyText()
	for _, want := range []string{
		wipeBodyGoes, wipeBodyItems, wipeBodyNoUndo, wipeBodyStays, wipeBodySavedFiles,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the body does not state %q", want)
		}
	}
}

func captureLog(t *testing.T, fn func()) []slog.Record {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	c := &recordCollector{}
	slog.SetDefault(slog.New(c))
	fn()
	return c.records
}

type recordCollector struct{ records []slog.Record }

func (c *recordCollector) Enabled(context.Context, slog.Level) bool { return true }
func (c *recordCollector) Handle(_ context.Context, r slog.Record) error {
	c.records = append(c.records, r)
	return nil
}
func (c *recordCollector) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *recordCollector) WithGroup(string) slog.Handler      { return c }

func logHas(records []slog.Record, want string) bool {
	for _, r := range records {
		if strings.Contains(r.Message, want) {
			return true
		}
	}
	return false
}

func TestWipeDevice_TheGuardLogsWhatItDecided(t *testing.T) {
	const wrongPIN = "0913"

	for _, c := range []struct {
		name string
		app  *App
		word string
		want string
	}{
		{"no vault", &App{}, wipeConfirmWord, "there is no vault"},
		{"wrong word", &App{VaultCtl: stubVaultInPackage{}}, "nope", "the confirmation word was wrong"},
		{"wrong PIN", &App{VaultCtl: stubVaultInPackage{}}, wipeConfirmWord, "the PIN was wrong"},
	} {
		t.Run(c.name, func(t *testing.T) {
			records := captureLog(t, func() {
				c.app.submitWipeDevice(wrongPIN, c.word, func() {})
			})
			if !logHas(records, c.want) {
				t.Fatalf("no line says %q; lines: %v", c.want, messages(records))
			}

			for _, r := range records {
				if strings.Contains(r.Message, wrongPIN) {
					t.Fatalf("the PIN reached the log's message: %q", r.Message)
				}
				r.Attrs(func(at slog.Attr) bool {
					if strings.Contains(at.Value.String(), wrongPIN) {
						t.Fatalf("the PIN reached the log as %q", at.Key)
					}
					return true
				})
			}
		})
	}
}

func messages(records []slog.Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Message)
	}
	return out
}
