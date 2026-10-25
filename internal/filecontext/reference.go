package filecontext

import (
	"context"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxReferenceTextBytes = 256 << 10
	maxMessageReferences  = 24
)

type ReferenceKind string

const (
	ReferenceKindMarkdownLink ReferenceKind = "markdown_link"
	ReferenceKindPlainPath    ReferenceKind = "plain_path"
)

type ReferenceReadRequest struct {
	ThreadID      string        `json:"threadId"`
	TurnID        string        `json:"turnId"`
	ItemID        string        `json:"itemId"`
	ReferenceKind ReferenceKind `json:"referenceKind"`
	RawReference  string        `json:"rawReference"`
	StartLine     int           `json:"startLine,omitempty"`
	EndLine       int           `json:"endLine,omitempty"`
}

var (
	colonLocation = regexp.MustCompile(`^(.*):([1-9][0-9]*)(?::([1-9][0-9]*))?$`)
	hashLocation  = regexp.MustCompile(`^(.*)#L([1-9][0-9]*)(?:-L([1-9][0-9]*))?$`)
	htmlTag       = regexp.MustCompile(`(?i)<\s*(/?)\s*([a-z][a-z0-9:-]*)\b[^>]*>`)
	markdownLink  = regexp.MustCompile(`\]\((?:<([^>\r\n]+)>|([^\s()\r\n]+))\)`)
	plainDocument = regexp.MustCompile(`(?:\.{0,2}/|/)?(?:[^\s()[\]{}<>"'，。；;：:]+/)*[^\s/()[\]{}<>"'，。；;：:]+\.[\pL\pN]{1,16}(?::[1-9][0-9]*(?::[1-9][0-9]*)?|#L[1-9][0-9]*(?:-L[1-9][0-9]*)?)?`)
)

func (s *Service) ReferenceRead(ctx context.Context, req ReferenceReadRequest) (ReadResponse, error) {
	if s == nil || s.source == nil || req.ThreadID == "" || req.TurnID == "" || req.ItemID == "" || len(req.RawReference) == 0 || len(req.RawReference) > 4096 || !utf8.ValidString(req.RawReference) || strings.IndexFunc(req.RawReference, unicode.IsControl) >= 0 {
		return ReadResponse{}, fail(CodeInvalidArgument)
	}
	if req.ReferenceKind != ReferenceKindMarkdownLink && req.ReferenceKind != ReferenceKindPlainPath {
		return ReadResponse{}, fail(CodeInvalidArgument)
	}
	thread, err := s.source.Thread(ctx, req.ThreadID)
	if err != nil || thread.ID != req.ThreadID || thread.CWD == "" {
		return ReadResponse{}, fail(CodeThreadNotFound)
	}
	items, err := s.source.Items(ctx, req.ThreadID, req.TurnID)
	if err != nil {
		return ReadResponse{}, fail(CodeItemNotFound)
	}
	var message *Item
	for index := range items {
		if items[index].ID == req.ItemID {
			if items[index].Type != "agentMessage" {
				return ReadResponse{}, fail(CodeItemNotFound)
			}
			message = &items[index]
			break
		}
	}
	if message == nil || len(message.Text) > maxReferenceTextBytes {
		return ReadResponse{}, fail(CodeItemNotFound)
	}
	if !referenceDeclaredWithinLimit(message.Text, req.ReferenceKind, req.RawReference) {
		return ReadResponse{}, fail(CodeReferenceNotFound)
	}
	path, _, _, ok := referencePath(req.RawReference)
	if !ok {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || decoded == "" || strings.ContainsRune(decoded, 0) {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	rel, ok := normalizeNativePath(thread.CWD, decoded)
	if !ok || !documentLikePath(rel) {
		return ReadResponse{}, fail(CodePathNotAllowed)
	}
	return s.readCurrentFile(ctx, thread.CWD, rel, nil)
}

func referencePath(raw string) (string, int, int, bool) {
	if match := hashLocation.FindStringSubmatch(raw); match != nil {
		start, _ := strconv.Atoi(match[2])
		end := start
		if match[3] != "" {
			end, _ = strconv.Atoi(match[3])
		}
		return match[1], start, end, match[1] != "" && end >= start
	}
	if match := colonLocation.FindStringSubmatch(raw); match != nil {
		line, _ := strconv.Atoi(match[2])
		return match[1], line, line, match[1] != ""
	}
	return raw, 0, 0, raw != ""
}

func documentLikePath(path string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "" || len(ext) > 16 {
		return false
	}
	for _, r := range ext {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func rawMarkdownReferenceDeclared(text, raw string) bool {
	return strings.Contains(text, "]("+raw+")") || strings.Contains(text, "](<"+raw+">)")
}

func markdownReferenceDeclared(text, raw string) bool {
	if !rawMarkdownReferenceDeclared(text, raw) {
		return false
	}
	for from := 0; from <= len(text)-len(raw); {
		index := strings.Index(text[from:], raw)
		if index < 0 {
			break
		}
		index += from
		before := text[:index]
		after := text[index+len(raw):]
		declared := strings.HasSuffix(before, "](") && strings.HasPrefix(after, ")")
		declared = declared || strings.HasSuffix(before, "](<") && strings.HasPrefix(after, ">)")
		if declared && !excludedCodeReference(text, index) {
			return true
		}
		from = index + 1
	}
	return false
}

func plainReferenceDeclared(text, raw string) bool {
	if strings.ContainsAny(raw, "\r\n\t") || strings.Contains(raw, "://") || strings.Contains(raw, "@") {
		return false
	}
	path, _, _, ok := referencePath(raw)
	if !ok || !documentLikePath(path) {
		return false
	}
	for from := 0; from <= len(text)-len(raw); {
		index := strings.Index(text[from:], raw)
		if index < 0 {
			return false
		}
		index += from
		end := index + len(raw)
		if referenceBoundary(text, index, end) && !excludedPlainReference(text, index, end) {
			return true
		}
		from = index + 1
	}
	return false
}

type parsedReference struct {
	start int
	kind  ReferenceKind
	raw   string
}

func referenceDeclaredWithinLimit(text string, kind ReferenceKind, raw string) bool {
	for _, reference := range parseMessageReferences(text) {
		if reference.kind == kind && reference.raw == raw {
			return true
		}
	}
	return false
}

func parseMessageReferences(text string) []parsedReference {
	all := make([]parsedReference, 0, maxMessageReferences)
	for _, match := range markdownLink.FindAllStringSubmatchIndex(text, -1) {
		start, end := match[2], match[3]
		angle := start >= 0
		if !angle {
			start, end = match[4], match[5]
		}
		raw := text[start:end]
		htmlStart := start
		if angle {
			htmlStart--
		}
		if referenceCandidate(raw) && !excludedCodeOnlyReference(text, start) && !insideRawHTML(text, htmlStart) {
			all = append(all, parsedReference{start: start, kind: ReferenceKindMarkdownLink, raw: raw})
		}
	}
	for _, match := range plainDocument.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		raw := text[start:end]
		if referenceCandidate(raw) && referenceBoundary(text, start, end) && !excludedPlainReference(text, start, end) {
			all = append(all, parsedReference{start: start, kind: ReferenceKindPlainPath, raw: raw})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].start < all[j].start })
	if len(all) > maxMessageReferences {
		all = all[:maxMessageReferences]
	}
	return all
}

func referenceCandidate(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, "\r\n\t") || strings.Contains(raw, "://") || strings.Contains(raw, "@") || strings.Contains(raw, "\\") {
		return false
	}
	path, _, _, ok := referencePath(raw)
	return ok && documentLikePath(path)
}

func referenceBoundary(text string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if referenceRune(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if referenceRune(r) {
			return false
		}
	}
	return true
}

func referenceRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._-/\\@:#", r)
}

func excludedPlainReference(text string, start, end int) bool {
	if excludedCodeReference(text, start) {
		return true
	}
	lineStart := strings.LastIndex(text[:start], "\n") + 1
	lineEnd := len(text)
	if next := strings.Index(text[end:], "\n"); next >= 0 {
		lineEnd = end + next
	}
	line := text[lineStart:lineEnd]
	relStart, relEnd := start-lineStart, end-lineStart
	if open := strings.LastIndex(line[:relStart], "]("); open >= 0 {
		if close := strings.Index(line[relEnd:], ")"); close >= 0 {
			return true
		}
	}
	tokenStart := relStart
	for tokenStart > 0 && !unicode.IsSpace(rune(line[tokenStart-1])) {
		tokenStart--
	}
	tokenEnd := relEnd
	for tokenEnd < len(line) && !unicode.IsSpace(rune(line[tokenEnd])) {
		tokenEnd++
	}
	token := line[tokenStart:tokenEnd]
	return strings.Contains(token, "://") || strings.Contains(token, "@")
}

func excludedCodeReference(text string, start int) bool {
	return excludedCodeOnlyReference(text, start) || insideRawHTML(text, start)
}

func excludedCodeOnlyReference(text string, start int) bool {
	lineStart := strings.LastIndex(text[:start], "\n") + 1
	lineEnd := len(text)
	if next := strings.Index(text[start:], "\n"); next >= 0 {
		lineEnd = start + next
	}
	line := text[lineStart:lineEnd]
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		return true
	}
	beforeAll := text[:start]
	if strings.Count(beforeAll, "```")%2 == 1 || strings.Count(beforeAll, "~~~")%2 == 1 {
		return true
	}
	return strings.Count(line[:start-lineStart], "`")%2 == 1
}

func insideRawHTML(text string, start int) bool {
	prefix := text[:start]
	if strings.LastIndex(prefix, "<") > strings.LastIndex(prefix, ">") {
		return true
	}
	depth := 0
	for _, match := range htmlTag.FindAllStringSubmatch(prefix, -1) {
		name := strings.ToLower(match[2])
		if match[1] == "/" {
			if depth > 0 {
				depth--
			}
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(match[0]), "/>") && !htmlVoidElement(name) {
			depth++
		}
	}
	return depth > 0
}

func htmlVoidElement(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}
