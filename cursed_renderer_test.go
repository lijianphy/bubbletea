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
