package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yumlevi/spore-code/internal/config"
)

func TestBracketedPasteInsertsImmediately(t *testing.T) {
	m := inputTestModel(t)

	next, _ := m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("hello\nworld"),
		Paste: true,
	})
	got := next.(*Model)

	if got.input.Value() != "hello\nworld" {
		t.Fatalf("paste was not inserted as one value: %q", got.input.Value())
	}
	if len(got.inputBurst) != 0 {
		t.Fatalf("paste should not leave buffered input: %#v", got.inputBurst)
	}
}

func TestTypedRunesFlushBeforeEnterSends(t *testing.T) {
	m := inputTestModel(t)

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	m = next.(*Model)
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = next.(*Model)

	if got := m.input.Value(); got != "" {
		t.Fatalf("ordinary runes should be buffered before the timer flushes, got %q", got)
	}

	next, _ = m.Update(inputTextFlushMsg{seq: m.inputBurstSeq})
	m = next.(*Model)
	if got := m.input.Value(); got != "hi" {
		t.Fatalf("timer flush did not insert typed text, got %q", got)
	}

	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*Model)
	if len(m.messages) == 0 || m.messages[len(m.messages)-1].Text != "hi" {
		t.Fatalf("enter did not flush buffered text before send: %#v", m.messages)
	}
}

func TestInputBurstFlushIsDebouncedUntilLatestTimer(t *testing.T) {
	m := inputTestModel(t)

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = next.(*Model)
	firstSeq := m.inputBurstSeq
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = next.(*Model)

	next, _ = m.Update(inputTextFlushMsg{seq: firstSeq})
	m = next.(*Model)
	if got := m.input.Value(); got != "" {
		t.Fatalf("stale flush timer should not insert partial input, got %q", got)
	}

	next, _ = m.Update(inputTextFlushMsg{seq: m.inputBurstSeq})
	m = next.(*Model)
	if got := m.input.Value(); got != "ab" {
		t.Fatalf("latest flush did not insert full input burst, got %q", got)
	}
}

func TestStreamedMultilinePasteDoesNotSubmitEachLine(t *testing.T) {
	m := inputTestModel(t)

	next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
	m = next.(*Model)
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*Model)
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})
	m = next.(*Model)
	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*Model)

	if len(m.messages) != 0 {
		t.Fatalf("streamed paste newlines should not submit messages: %#v", m.messages)
	}

	next, _ = m.Update(inputTextFlushMsg{seq: m.inputBurstSeq})
	m = next.(*Model)
	if got := m.input.Value(); got != "first\nsecond\n" {
		t.Fatalf("streamed paste was not preserved as textarea content: %q", got)
	}

	next, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*Model)
	if len(m.messages) != 1 || m.messages[0].Text != "first\nsecond" {
		t.Fatalf("final enter should submit one multiline message: %#v", m.messages)
	}
}

func TestDroppedFileURIsNormalizeToPaths(t *testing.T) {
	m := inputTestModel(t)

	next, _ := m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("file:///tmp/spore%20shot.png file:///tmp/second%20shot.png"),
		Paste: true,
	})
	got := next.(*Model)

	if got.input.Value() != "/tmp/spore shot.png\n/tmp/second shot.png" {
		t.Fatalf("file URI was not decoded: %q", got.input.Value())
	}
}

func TestFileURIInProseStillNormalizes(t *testing.T) {
	got := normalizePastedInput("look at file:///tmp/spore%20shot.png please", "")
	want := "look at /tmp/spore shot.png please"
	if got != want {
		t.Fatalf("file URI in prose mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestShellQuotedFileDropNormalizesToNewlinePaths(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one image.png")
	second := filepath.Join(dir, "two.txt")
	touchFile(t, first)
	touchFile(t, second)

	raw := "'" + first + "' \"" + second + "\""
	got := normalizePastedInput(raw, dir)
	want := strings.Join([]string{
		"Attached image: one image.png",
		"Attached file: two.txt",
	}, "\n")
	if got != want {
		t.Fatalf("drop normalization mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestRawDroppedPathWithSpacesAttachesAsSingleImage(t *testing.T) {
	m := inputTestModel(t)
	src := filepath.Join(m.cwd, "spore shot.png")
	touchFile(t, src)

	for _, r := range src {
		next, _ := m.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*Model)
	}
	next, _ := m.Update(inputTextFlushMsg{seq: m.inputBurstSeq})
	got := next.(*Model)

	if got.input.Value() != "Attached image: spore shot.png" {
		t.Fatalf("expected raw path with spaces to become image placeholder, got %q", got.input.Value())
	}
	if len(got.inputAttachments) != 1 || got.inputAttachments[0].Path != src {
		t.Fatalf("expected hidden image attachment for raw path, got %#v", got.inputAttachments)
	}
}

func TestDroppedImageOutsideProjectCopiesToAttachment(t *testing.T) {
	m := inputTestModel(t)
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "spore shot.png")
	touchFile(t, src)

	next, _ := m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(pathToFileURI(src)),
		Paste: true,
	})
	got := next.(*Model)

	if got.input.Value() != "Attached image: spore shot.png" {
		t.Fatalf("expected image placeholder without a path, got %q", got.input.Value())
	}
	if len(got.inputAttachments) != 1 {
		t.Fatalf("expected one hidden image attachment, got %#v", got.inputAttachments)
	}
	attachment := got.inputAttachments[0]
	if attachment.Kind != "image" || attachment.Name != "spore shot.png" || attachment.MediaType != "image/png" {
		t.Fatalf("unexpected image attachment metadata: %#v", attachment)
	}
	rel, err := filepath.Rel(got.cwd, attachment.Path)
	if err != nil {
		t.Fatalf("attachment path was not relative to cwd: %v", err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".spore-code/attachments/") {
		t.Fatalf("expected project-local attachment path, got %q", rel)
	}
	if _, err := os.Stat(attachment.Path); err != nil {
		t.Fatalf("attachment was not copied into project: %v", err)
	}
}

func TestDroppedImageBuildsChatImagePayload(t *testing.T) {
	m := inputTestModel(t)
	src := filepath.Join(m.cwd, "spore shot.png")
	touchFile(t, src)

	next, _ := m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(pathToFileURI(src)),
		Paste: true,
	})
	got := next.(*Model)

	payload := got.chatPayload("look", "look", nil, inputAttachmentsForText(got.input.Value(), got.inputAttachments)...)
	rawImages, ok := payload["images"].([]map[string]any)
	if !ok || len(rawImages) != 1 {
		t.Fatalf("expected one chat image payload, got %#v", payload["images"])
	}
	image := rawImages[0]
	if image["name"] != "spore shot.png" || image["mediaType"] != "image/png" {
		t.Fatalf("unexpected image payload metadata: %#v", image)
	}
	if image["data"] != base64.StdEncoding.EncodeToString([]byte("x")) {
		t.Fatalf("unexpected image payload data: %#v", image["data"])
	}
}

func TestDeletedImagePlaceholderDoesNotSendHiddenAttachment(t *testing.T) {
	m := inputTestModel(t)
	src := filepath.Join(m.cwd, "spore shot.png")
	touchFile(t, src)
	attachments := []inputAttachment{{
		Kind:      "image",
		Name:      "spore shot.png",
		Path:      src,
		MediaType: "image/png",
	}}

	payload := m.chatPayload("look", "look", nil, inputAttachmentsForText("look", attachments)...)
	if _, ok := payload["images"]; ok {
		t.Fatalf("deleted placeholder should not send images: %#v", payload["images"])
	}
}

func TestQuotedWindowsPathDropKeepsBackslashes(t *testing.T) {
	got := normalizePastedInput(`"C:\Users\Levi\Pictures\spore shot.png"`, "")
	want := `C:\Users\Levi\Pictures\spore shot.png`
	if got != want {
		t.Fatalf("windows path normalization mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestRawWindowsPathDropKeepsBackslashes(t *testing.T) {
	got := normalizePastedInput(`C:\Users\Levi\Pictures\spore shot.png`, "")
	want := `C:\Users\Levi\Pictures\spore shot.png`
	if got != want {
		t.Fatalf("raw windows path normalization mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestOpenQuestionPasteUsesFastPath(t *testing.T) {
	m := inputTestModel(t)
	m.modal = modalQuestion
	m.question = &questionModal{
		questions: []question{{Text: "Path?"}},
		answers:   make([]string, 1),
		checked:   map[int]bool{},
	}

	next, _ := m.updateQuestionModal(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("file:///tmp/example%20image.png"),
		Paste: true,
	})
	got := next.(*Model)

	if got.input.Value() != "/tmp/example image.png" {
		t.Fatalf("question paste was not normalized: %q", got.input.Value())
	}
}

func TestPlanFeedbackPasteUsesFastPath(t *testing.T) {
	m := inputTestModel(t)
	m.modal = modalPlan
	m.planApproval = &planModal{noting: true}
	feedback := "to be clear, the cli tier uses the routing we have now"

	next, _ := m.updatePlanModal(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(feedback),
		Paste: true,
	})
	got := next.(*Model)

	if got.planApproval.feedback != feedback {
		t.Fatalf("plan feedback paste mismatch: %q", got.planApproval.feedback)
	}
	if strings.Contains(got.planApproval.feedback, "[") || strings.Contains(got.planApproval.feedback, "]") {
		t.Fatalf("plan feedback paste should not use KeyMsg bracket rendering: %q", got.planApproval.feedback)
	}
}

func TestPlanFeedbackFlushesBeforeSubmit(t *testing.T) {
	m := inputTestModel(t)
	m.modal = modalPlan
	m.planApproval = &planModal{noting: true}
	feedback := "to be clear, the cli tier uses the routing we have now"

	next, _ := m.updatePlanModal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(feedback)})
	m = next.(*Model)
	if m.planApproval.feedback != "" {
		t.Fatalf("feedback should be pending in input burst before flush, got %q", m.planApproval.feedback)
	}

	next, _ = m.updatePlanModal(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*Model)
	if len(m.messages) == 0 || !strings.Contains(m.messages[len(m.messages)-1].Text, feedback) {
		t.Fatalf("submit did not flush full feedback into chat: %#v", m.messages)
	}
}

func inputTestModel(t *testing.T) *Model {
	t.Helper()
	ta := textarea.New()
	ta.Focus()
	return &Model{
		cfg:              &config.Config{GlobalDir: t.TempDir()},
		cwd:              t.TempDir(),
		sess:             "test-session",
		input:            ta,
		currentStreamIdx: -1,
		histIdx:          -1,
		followBottom:     true,
	}
}

func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
}

func pathToFileURI(path string) string {
	u := "file://" + filepath.ToSlash(path)
	return strings.ReplaceAll(u, " ", "%20")
}
