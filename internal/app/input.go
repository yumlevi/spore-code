package app

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

const inputBurstDelay = 12 * time.Millisecond

type inputTextFlushMsg struct {
	seq uint64
}

type inputAttachment struct {
	Kind      string
	Name      string
	Path      string
	MediaType string
}

var fileURIRe = regexp.MustCompile(`file://[^\s"'<>]+`)

func (m *Model) handleTextInputKey(km tea.KeyMsg) (tea.Cmd, bool) {
	if m.bufferedPasteNewline(km) {
		return m.queueInputText("\n", true), true
	}

	if km.Paste {
		if text := keyText(km); text != "" {
			m.flushPendingInputText()
			m.insertInputText(m.normalizeInputText(text))
			return nil, true
		}
	}

	text := keyText(km)
	if text == "" {
		return nil, false
	}
	return m.queueInputText(text, len([]rune(text)) > 1), true
}

func (m *Model) bufferedPasteNewline(km tea.KeyMsg) bool {
	if km.Alt || len(m.inputBurst) == 0 || !m.inputBurstScheduled {
		return false
	}
	return km.Type == tea.KeyEnter || km.Type == tea.KeyCtrlJ
}

func keyText(km tea.KeyMsg) string {
	if km.Alt {
		return ""
	}
	switch km.Type {
	case tea.KeyRunes:
		if len(km.Runes) == 0 {
			return ""
		}
		return string(km.Runes)
	case tea.KeySpace:
		return " "
	default:
		return ""
	}
}

func (m *Model) queueInputText(text string, normalize bool) tea.Cmd {
	if text == "" {
		return nil
	}
	m.inputBurst = append(m.inputBurst, []rune(text)...)
	if normalize {
		m.inputBurstNormalize = true
	}
	m.inputBurstScheduled = true
	m.inputBurstSeq++
	seq := m.inputBurstSeq
	return func() tea.Msg {
		time.Sleep(inputBurstDelay)
		return inputTextFlushMsg{seq: seq}
	}
}

func (m *Model) flushPendingInputText() {
	if len(m.inputBurst) == 0 {
		m.inputBurstScheduled = false
		m.inputBurstNormalize = false
		return
	}
	text := string(m.inputBurst)
	if m.inputBurstNormalize || len(m.inputBurst) >= 16 {
		text = m.normalizeInputText(text)
	}
	m.inputBurst = nil
	m.inputBurstScheduled = false
	m.inputBurstNormalize = false
	m.insertInputText(text)
}

func (m *Model) insertInputText(text string) {
	if text == "" {
		return
	}
	if m.modal == modalPlan && m.planApproval != nil && m.planApproval.noting {
		m.planApproval.feedback += text
		return
	}
	m.input.InsertString(text)
	if m.modal == modalNone {
		m.refreshSuggest()
	}
}

func (m *Model) normalizeInputText(raw string) string {
	text, attachments := normalizePastedInputWithAttachments(raw, m.cwd, m.modal == modalNone)
	if m.modal == modalNone && len(attachments) > 0 {
		m.inputAttachments = append(m.inputAttachments, attachments...)
	}
	return text
}

func inputAttachmentsForText(text string, attachments []inputAttachment) []inputAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]inputAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Kind == "image" && attachment.Name != "" {
			if !strings.Contains(text, "Attached image: "+attachment.Name) {
				continue
			}
		}
		out = append(out, attachment)
	}
	return out
}

func normalizePastedInput(raw, cwd string) string {
	text, _ := normalizePastedInputWithAttachments(raw, cwd, false)
	return text
}

func normalizePastedInputWithAttachments(raw, cwd string, attachImages bool) (string, []inputAttachment) {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if paths, ok := droppedPathFields(s, cwd); ok {
		lines, attachments := materializeDroppedPaths(paths, cwd, attachImages)
		return strings.Join(lines, "\n"), attachments
	}
	s = fileURIRe.ReplaceAllStringFunc(s, func(token string) string {
		path, ok := fileURIToPath(token)
		if !ok {
			return token
		}
		return path
	})
	return s, nil
}

func droppedPathFields(s, cwd string) ([]string, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, false
	}
	if path, ok := fileURIToPath(trimmed); ok && !strings.Contains(path, "file://") {
		return []string{path}, true
	}
	if pathExists(trimmed, cwd) || (looksLikeDroppedPath(trimmed, cwd) && !strings.ContainsAny(trimmed, " \t\n")) {
		return []string{trimmed}, true
	}

	fields, ok := shellLikeFields(s)
	if !ok || len(fields) == 0 {
		return nil, false
	}
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if path, ok := fileURIToPath(field); ok {
			paths = append(paths, path)
			continue
		}
		if looksLikeDroppedPath(field, cwd) {
			paths = append(paths, field)
			continue
		}
		return nil, false
	}
	return paths, true
}

func fileURIToPath(token string) (string, bool) {
	u, err := url.Parse(token)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	path, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		path = u.Path
	}
	if u.Host != "" && u.Host != "localhost" {
		return "//" + u.Host + path, true
	}
	if isWindowsDrivePath(strings.TrimPrefix(path, "/")) {
		return strings.TrimPrefix(path, "/"), true
	}
	return path, true
}

func materializeDroppedPaths(paths []string, cwd string, attachImages bool) ([]string, []inputAttachment) {
	out := make([]string, 0, len(paths))
	attachments := make([]inputAttachment, 0, len(paths))
	for _, p := range paths {
		line, attachment := materializeDroppedPath(p, cwd, attachImages)
		out = append(out, line)
		if attachment != nil {
			attachments = append(attachments, *attachment)
		}
	}
	return out, attachments
}

func materializeDroppedPath(path, cwd string, attachImages bool) (string, *inputAttachment) {
	resolved := expandHome(path)
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return path, nil
	}
	label := "file"
	isImage := isImagePath(resolved)
	if isImage {
		label = "image"
	}
	usable := usablePathForAgent(resolved, cwd)
	if usable == "" && cwd != "" {
		if rel, err := copyDroppedFileIntoProject(resolved, cwd); err == nil {
			usable = rel
		}
	}
	if usable == "" {
		usable = path
	}
	if isImage && attachImages {
		attachPath := resolvedAttachmentPath(usable, resolved, cwd)
		attachment := &inputAttachment{
			Kind:      "image",
			Name:      filepath.Base(resolved),
			Path:      attachPath,
			MediaType: mediaTypeForPath(resolved),
		}
		return fmt.Sprintf("Attached image: %s", filepath.Base(resolved)), attachment
	}
	return fmt.Sprintf("Attached %s: %s", label, filepath.ToSlash(usable)), nil
}

func resolvedAttachmentPath(usable, fallback, cwd string) string {
	if cwd != "" && usable != "" && !filepath.IsAbs(usable) && !isWindowsDrivePath(usable) {
		p := filepath.Join(cwd, filepath.FromSlash(usable))
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return fallback
}

func usablePathForAgent(path, cwd string) string {
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

func copyDroppedFileIntoProject(src, cwd string) (string, error) {
	const maxAttachmentBytes int64 = 50 * 1024 * 1024
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("cannot attach directory")
	}
	if info.Size() > maxAttachmentBytes {
		return "", fmt.Errorf("attachment too large")
	}
	dir := filepath.Join(cwd, ".spore-code", "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := uniqueAttachmentPath(dir, filepath.Base(src))
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(cwd, dst)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func uniqueAttachmentPath(dir, base string) string {
	base = safeAttachmentBase(base)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	ext := filepath.Ext(base)
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}
}

func safeAttachmentBase(base string) string {
	base = filepath.Base(base)
	base = strings.TrimSpace(base)
	if base == "." || base == "" {
		return "attachment"
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), ".-")
	if clean == "" {
		return "attachment"
	}
	return clean
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}

func isImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".heif", ".svg":
		return true
	default:
		return false
	}
}

func mediaTypeForPath(path string) string {
	if typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); typ != "" {
		return strings.Split(typ, ";")[0]
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

func chatImagesFromAttachments(attachments []inputAttachment) []map[string]any {
	if len(attachments) == 0 {
		return nil
	}
	seen := map[string]bool{}
	images := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Kind != "image" || attachment.Path == "" || seen[attachment.Path] {
			continue
		}
		data, err := os.ReadFile(attachment.Path)
		if err != nil || len(data) == 0 {
			continue
		}
		seen[attachment.Path] = true
		mediaType := attachment.MediaType
		if mediaType == "" {
			mediaType = mediaTypeForPath(attachment.Path)
		}
		images = append(images, map[string]any{
			"name":      attachment.Name,
			"mediaType": mediaType,
			"data":      base64.StdEncoding.EncodeToString(data),
		})
	}
	return images
}

func shellLikeFields(s string) ([]string, bool) {
	var fields []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	haveField := false

	flush := func() {
		if haveField {
			fields = append(fields, b.String())
			b.Reset()
			haveField = false
		}
	}

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			haveField = true
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle && !inDouble:
			if isWindowsPathFieldPrefix(b.String()) {
				b.WriteRune(r)
			} else {
				escaped = true
			}
			haveField = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			haveField = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			haveField = true
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			flush()
		default:
			b.WriteRune(r)
			haveField = true
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if inSingle || inDouble {
		return nil, false
	}
	flush()
	return fields, true
}

func isWindowsPathFieldPrefix(s string) bool {
	if len(s) >= 2 {
		b := []byte(s)
		if ((b[0] >= 'a' && b[0] <= 'z') || (b[0] >= 'A' && b[0] <= 'Z')) && b[1] == ':' {
			return true
		}
	}
	return s == `\` || strings.HasPrefix(s, `\\`)
}

func looksLikeDroppedPath(path, cwd string) bool {
	if path == "" {
		return false
	}
	if pathExists(path, cwd) {
		return true
	}
	if filepath.IsAbs(path) || isWindowsDrivePath(path) || strings.HasPrefix(path, `\\`) {
		return true
	}
	return strings.HasPrefix(path, "./") ||
		strings.HasPrefix(path, "../") ||
		strings.HasPrefix(path, `.\`) ||
		strings.HasPrefix(path, `..\`) ||
		strings.HasPrefix(path, "~/") ||
		strings.HasPrefix(path, `~\`)
}

func pathExists(path, cwd string) bool {
	if _, err := os.Stat(expandHome(path)); err == nil {
		return true
	}
	if cwd != "" && !filepath.IsAbs(path) && !isWindowsDrivePath(path) {
		if _, err := os.Stat(filepath.Join(cwd, path)); err == nil {
			return true
		}
	}
	return false
}

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func isWindowsDrivePath(path string) bool {
	if utf8.RuneCountInString(path) < 3 {
		return false
	}
	b := []byte(path)
	return ((b[0] >= 'a' && b[0] <= 'z') || (b[0] >= 'A' && b[0] <= 'Z')) &&
		b[1] == ':' &&
		(b[2] == '\\' || b[2] == '/')
}
