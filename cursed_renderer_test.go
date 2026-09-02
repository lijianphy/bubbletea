package tea

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type mouseRaceModel struct {
	i int
}

func (m *mouseRaceModel) Init() Cmd { return nil }

func (m *mouseRaceModel) Update(msg Msg) (Model, Cmd) {
	switch msg.(type) {
	case MouseClickMsg, MouseMotionMsg, MouseWheelMsg:
		m.i++
	}
	return m, nil
}

func (m *mouseRaceModel) View() View {
	return View{
		Content:   fmt.Sprintf("tick-%d\n", m.i),
		MouseMode: MouseModeCellMotion,
	}
}

// Fixes: https://github.com/charmbracelet/bubbletea/issues/1690
func TestCursedRenderer_mouseVsFlush(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	m := &mouseRaceModel{}
	p := NewProgram(
		m,
		WithContext(t.Context()),
		WithInput(pr),
		WithOutput(io.Discard),
		WithEnvironment([]string{
			"TERM=xterm-256color",
			"TERM_PROGRAM=Apple_Terminal",
		}),
		WithoutSignals(),
		WithWindowSize(80, 24),
	)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = p.Run()
	}()

	time.Sleep(150 * time.Millisecond)

	const iterations = 100
	for i := range iterations {
		switch i % 4 {
		case 0:
			p.Send(MouseClickMsg{X: i % 80, Y: i % 24, Button: MouseLeft})
		case 1:
			p.Send(MouseMotionMsg{X: i % 80, Y: i % 24})
		case 2:
			p.Send(MouseWheelMsg{X: 0, Y: 0, Button: MouseWheelUp})
		default:
			p.Send(MouseReleaseMsg{X: i % 80, Y: i % 24, Button: MouseLeft})
		}
	}

	p.Quit()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("program did not exit after Quit")
	}
}

func TestCursedRenderer_insertAboveAfterRenderFlushesPendingFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	renderer.render(NewView("old"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush old frame: %v", err)
	}

	out.Reset()
	renderer.render(NewView("new"))
	if err := renderer.insertAboveAfterRender("committed scrollback"); err != nil {
		t.Fatalf("insert above after render: %v", err)
	}

	raw := out.String()
	frameIndex := strings.Index(raw, "new")
	scrollbackIndex := strings.Index(raw, "committed scrollback")
	if frameIndex < 0 {
		t.Fatalf("output missing pending frame flush: %q", raw)
	}
	if scrollbackIndex < 0 {
		t.Fatalf("output missing inserted scrollback: %q", raw)
	}
	if frameIndex > scrollbackIndex {
		t.Fatalf("output inserted scrollback before pending frame flush: %q", raw)
	}
}

func TestCursedRenderer_insertAboveAfterRenderSuppressesAltScreenOutput(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	renderer.render(NewView("old"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush old frame: %v", err)
	}

	out.Reset()
	view := NewView("altscreen frame")
	view.AltScreen = true
	renderer.render(view)
	if err := renderer.insertAboveAfterRender("committed scrollback"); err != nil {
		t.Fatalf("insert above after render: %v", err)
	}

	if raw := out.String(); raw != "" {
		t.Fatalf("altscreen after-render print wrote output: %q", raw)
	}
}

func TestCursedRenderer_insertAboveAfterRenderUsesOneSynchronizedOutputBlock(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	renderer.syncdUpdates = true
	renderer.render(NewView("old"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush old frame: %v", err)
	}

	out.Reset()
	renderer.render(NewView("new"))
	if err := renderer.insertAboveAfterRender("committed scrollback"); err != nil {
		t.Fatalf("insert above after render: %v", err)
	}

	raw := out.String()
	startIndex := strings.Index(raw, ansi.SetModeSynchronizedOutput)
	resetIndex := strings.LastIndex(raw, ansi.ResetModeSynchronizedOutput)
	frameIndex := strings.Index(raw, "new")
	scrollbackIndex := strings.Index(raw, "committed scrollback")
	if startIndex < 0 || resetIndex < 0 {
		t.Fatalf("output missing synchronized output wrapper: %q", raw)
	}
	if frameIndex < 0 || scrollbackIndex < 0 {
		t.Fatalf("output missing frame or scrollback: %q", raw)
	}
	if startIndex > frameIndex || resetIndex < scrollbackIndex {
		t.Fatalf("synchronized output wrapper does not enclose frame and scrollback: %q", raw)
	}
	if earlyReset := strings.Index(raw[:scrollbackIndex], ansi.ResetModeSynchronizedOutput); earlyReset >= 0 {
		t.Fatalf("synchronized output closed before scrollback insertion: %q", raw)
	}
}

func TestCursedRendererInlineShrinkDeletesOldFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	renderer.render(NewView("old 0\nold 1\nold 2\nold 3\nold 4\nold 5\nold 6"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush old frame: %v", err)
	}
	if strings.Contains(out.String(), ansi.DeleteLine(7)) {
		t.Fatal("initial inline frame deleted preexisting rows")
	}

	out.Reset()
	renderer.render(NewView("new 0\nnew 1"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush shrunken frame: %v", err)
	}

	raw := out.String()
	if !strings.Contains(raw, ansi.DeleteLine(7)) {
		t.Fatalf("inline shrink did not delete its 7-row old frame: %q", raw)
	}
}

func TestCursedRendererInlineHeightResizeDoesNotRedraw(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	view := NewView("frame 0\nframe 1\nframe 2")
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush initial frame: %v", err)
	}

	out.Reset()
	renderer.resize(40, 12)
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush height-only resize: %v", err)
	}
	if raw := out.String(); raw != "" {
		t.Fatalf("inline height-only resize redrew an unchanged frame: %q", raw)
	}
}

func TestCursedRendererPendingEraseRedrawsUnchangedFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	view := NewView("frame")
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush initial frame: %v", err)
	}

	out.Reset()
	renderer.clearScreen()
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush cleared frame: %v", err)
	}
	raw := out.String()
	if !strings.Contains(raw, ansi.EraseScreenBelow) || !strings.Contains(raw, "frame") {
		t.Fatalf("pending erase did not redraw unchanged frame: %q", raw)
	}
}

func TestCursedRenderer_insertAboveDoesNotEraseFullWidthLines(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 4, 4)
	if err := renderer.insertAbove("abcd\nabc"); err != nil {
		t.Fatalf("insert above: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, "abcd"+ansi.EraseLineRight) {
		t.Fatalf("full-width inserted line was erased on the right edge: %q", raw)
	}
	if !strings.Contains(raw, "abc"+ansi.EraseLineRight) {
		t.Fatalf("short inserted line was not cleared to the right edge: %q", raw)
	}
}

func TestCursedRenderer_insertAboveChunksPayloadTallerThanTerminal(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 20, 5)
	lines := []string{
		"line 00",
		"line 01",
		"line 02",
		"line 03",
		"line 04",
		"line 05",
		"line 06",
		"line 07",
		"line 08",
		"line 09",
		"line 10",
		"line 11",
	}
	if err := renderer.insertAbove(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("insert above: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, strings.Repeat("\n", 5)) {
		t.Fatalf("tall insert emitted a full-screen blank scroll before content: %q", raw)
	}
	if strings.Contains(raw, ansi.InsertLine(5)) || strings.Contains(raw, ansi.InsertLine(len(lines)+1)) {
		t.Fatalf("tall insert used an unsafe insert-line count: %q", raw)
	}
	lastIndex := -1
	for _, line := range lines {
		index := strings.Index(raw, line)
		if index < 0 {
			t.Fatalf("tall insert missing %q in %q", line, raw)
		}
		if index < lastIndex {
			t.Fatalf("tall insert reordered %q in %q", line, raw)
		}
		lastIndex = index
	}
}

func TestCursedRenderer_insertAboveUsesTerminalHeightForOneLineFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 20, 5)
	renderer.render(NewView("frame"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush frame: %v", err)
	}

	out.Reset()
	if err := renderer.insertAbove("line 00\nline 01\nline 02\nline 03"); err != nil {
		t.Fatalf("insert above: %v", err)
	}

	raw := out.String()
	if !strings.Contains(raw, ansi.InsertLine(4)) {
		t.Fatalf("one-line frame insert did not use terminal-height chunk: %q", raw)
	}
	if strings.Count(raw, ansi.InsertLine(1)) > 0 {
		t.Fatalf("one-line frame insert was split into one-row chunks: %q", raw)
	}
}

func TestCursedRenderer_insertAboveDoesNotUseRowsOccupiedByFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 20, 5)
	renderer.render(NewView("frame 1\nframe 2"))
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush frame: %v", err)
	}

	out.Reset()
	if err := renderer.insertAbove("line 00\nline 01\nline 02\nline 03"); err != nil {
		t.Fatalf("insert above: %v", err)
	}

	raw := out.String()
	if strings.Contains(raw, ansi.InsertLine(4)) {
		t.Fatalf("insert above used rows occupied by the frame: %q", raw)
	}
	if !strings.Contains(raw, ansi.InsertLine(3)) {
		t.Fatalf("insert above did not use spare terminal rows: %q", raw)
	}
}

func TestInsertAboveChunkLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		frameHeight    int
		terminalHeight int
		want           int
	}{
		{
			name:           "uses spare terminal rows above one-line frame",
			frameHeight:    1,
			terminalHeight: 5,
			want:           4,
		},
		{
			name:           "does not use rows occupied by multi-line frame",
			frameHeight:    2,
			terminalHeight: 5,
			want:           3,
		},
		{
			name:           "uses one-row chunks when frame fills terminal",
			frameHeight:    5,
			terminalHeight: 5,
			want:           1,
		},
		{
			name:           "uses one-row chunks when frame is taller than terminal",
			frameHeight:    10,
			terminalHeight: 5,
			want:           1,
		},
		{
			name:           "keeps full-screen safety margin without managed frame",
			frameHeight:    0,
			terminalHeight: 5,
			want:           4,
		},
		{
			name:           "falls back to frame height when terminal height is unknown",
			frameHeight:    3,
			terminalHeight: 0,
			want:           2,
		},
		{
			name:           "minimum is one",
			frameHeight:    0,
			terminalHeight: 0,
			want:           1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := insertAboveChunkLimit(tt.frameHeight, tt.terminalHeight); got != tt.want {
				t.Fatalf("insertAboveChunkLimit(%d, %d) = %d, want %d", tt.frameHeight, tt.terminalHeight, got, tt.want)
			}
		})
	}
}
func assertInOrder(t *testing.T, got string, wants ...string) {
	t.Helper()
	rest := got
	for _, want := range wants {
		idx := strings.Index(rest, want)
		if idx < 0 {
			t.Fatalf("expected %q to appear after the previous sequences in %q", want, got)
		}
		rest = rest[idx+len(want):]
	}
}

func TestCursedRenderer_restoresKittyKeyboardStack(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	r := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 80, 24)
	r.start()

	view := NewView("hello")
	view.KeyboardEnhancements.ReportEventTypes = true
	pushMain := ansi.PushKittyKeyboard(keyboardEnhancementsFlags(view.KeyboardEnhancements))
	pop := ansi.PopKittyKeyboard(1)

	render := func(v View) {
		t.Helper()
		r.render(v)
		if err := r.flush(false); err != nil {
			t.Fatal(err)
		}
	}

	render(view)

	// Stop the renderer (as on suspend or ExecProcess) and start it again:
	// close pops the stack entry, start pushes it back.
	if err := r.close(); err != nil {
		t.Fatal(err)
	}
	r.start()

	// Enter and leave the alt screen. The terminal keeps a separate Kitty
	// keyboard stack per screen, so each screen gets its own push and pop.
	view.AltScreen = true
	render(view)
	view.AltScreen = false
	render(view)

	if err := r.close(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	// The flags are pushed once per screen activation: the first flush,
	// the flush after the renderer was restarted, and on each screen
	// switch. start() itself does not write to [out].
	if n := strings.Count(got, pushMain); n != 4 {
		t.Fatalf("expected kitty keyboard protocol to be pushed 4 times with %q (%d times), got %q", pushMain, n, got)
	}
	// One pop per stop/start cycle and per screen switch: closing pops the
	// current screen's entry, and switching screens pops the entry of the
	// screen being left.
	if n := strings.Count(got, pop); n != 4 {
		t.Fatalf("expected kitty keyboard protocol to be popped 4 times with %q (%d times), got %q", pop, n, got)
	}
	// Every screen activation pushes exactly once before that screen is popped.
	assertInOrder(t, got,
		pushMain, pop, // initial main screen
		pushMain, pop, // resumed main screen
		pushMain, pop, // alt screen
		pushMain, pop, // main screen after leaving alt
	)
	if strings.Contains(got, ansi.KittyKeyboard(0, 1)) {
		t.Fatalf("expected kitty keyboard protocol not to be reset in-place with %q, got %q", ansi.KittyKeyboard(0, 1), got)
	}
}

func TestCursedRenderer_restoresKeyboardStackBeforeScreenSwitch(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 80, 24)
	renderer.start()

	view := NewView("hello")
	view.KeyboardEnhancements.ReportEventTypes = true
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush initial frame: %v", err)
	}
	if err := renderer.close(); err != nil {
		t.Fatalf("close renderer: %v", err)
	}

	out.Reset()
	renderer.start()
	view.AltScreen = true
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush alt-screen frame after restart: %v", err)
	}

	push := ansi.PushKittyKeyboard(keyboardEnhancementsFlags(view.KeyboardEnhancements))
	pop := ansi.PopKittyKeyboard(1)
	enterAlt := ansi.SetModeAltScreenSaveCursor
	raw := out.String()
	assertInOrder(t, raw, push, pop, enterAlt, push)

	enterAltIndex := strings.Index(raw, enterAlt)
	if got := strings.Count(raw[:enterAltIndex], push); got != 1 {
		t.Fatalf("main screen received %d restored keyboard pushes, want 1: %q", got, raw)
	}
	if got := strings.Count(raw[enterAltIndex+len(enterAlt):], push); got != 1 {
		t.Fatalf("alt screen received %d keyboard pushes, want 1: %q", got, raw)
	}
}

func TestCursedRenderer_updatesKittyKeyboardFlagsInPlace(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	r := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 80, 24)

	render := func(v View) {
		t.Helper()
		r.render(v)
		if err := r.flush(false); err != nil {
			t.Fatal(err)
		}
	}

	view := NewView("hello")
	render(view)

	// Changing the enhancement flags without switching screens updates the
	// current stack entry in place instead of pushing a new one.
	changed := view
	changed.KeyboardEnhancements.ReportEventTypes = true
	render(changed)

	wantUpdate := ansi.KittyKeyboard(keyboardEnhancementsFlags(changed.KeyboardEnhancements), 1)
	got := out.String()
	if !strings.Contains(got, wantUpdate) {
		t.Fatalf("expected kitty keyboard flags to be updated in place with %q, got %q", wantUpdate, got)
	}
	assertInOrder(t, got,
		ansi.PushKittyKeyboard(keyboardEnhancementsFlags(view.KeyboardEnhancements)),
		wantUpdate,
	)
	if strings.Contains(got, ansi.PopKittyKeyboard(1)) {
		t.Fatalf("expected kitty keyboard protocol not to be popped with %q, got %q", ansi.PopKittyKeyboard(1), got)
	}
	if n := strings.Count(got, ansi.PushKittyKeyboard(0)); n > 1 {
		t.Fatalf("expected kitty keyboard protocol to be pushed once, got %d pushes in %q", n, got)
	}
}

func TestCursedRenderer_restoresGraphemeWidthAfterRestart(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := newCursedRenderer(&out, []string{"TERM=xterm-256color"}, 40, 10)
	view := NewView("hello")
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush initial frame: %v", err)
	}

	out.Reset()
	renderer.setWidthMethod(ansi.GraphemeWidth)
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush grapheme-width update: %v", err)
	}
	if raw := out.String(); !strings.Contains(raw, ansi.SetModeUnicodeCore) {
		t.Fatalf("unchanged frame did not flush Unicode core mode: %q", raw)
	}

	out.Reset()
	if err := renderer.close(); err != nil {
		t.Fatalf("close renderer: %v", err)
	}
	if raw := out.String(); !strings.Contains(raw, ansi.ResetModeUnicodeCore) {
		t.Fatalf("close did not reset Unicode core mode: %q", raw)
	}

	out.Reset()
	renderer.start()
	renderer.render(view)
	if err := renderer.flush(false); err != nil {
		t.Fatalf("flush restarted renderer: %v", err)
	}
	if raw := out.String(); !strings.Contains(raw, ansi.SetModeUnicodeCore) {
		t.Fatalf("restart did not restore Unicode core mode: %q", raw)
	}
}
