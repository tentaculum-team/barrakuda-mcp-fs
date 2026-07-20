package mcp

import (
	"strings"
	"testing"

	"barrakuda-mcp-fs/internal/domain"
)

func TestFormatReadDefaultShowsWholeSmallFile(t *testing.T) {
	res := domain.ReadResult{Content: "a\nb\nc", SizeBytes: 5}
	out := formatRead("f.txt", res, 1, 0)
	if !strings.Contains(out, "lines 1-3 of 3") {
		t.Fatalf("missing range header: %q", out)
	}
	if !strings.Contains(out, "1→a\n2→b\n3→c\n") {
		t.Fatalf("missing numbered lines: %q", out)
	}
	if strings.Contains(out, "TRUNCATED") {
		t.Fatalf("small file must not be truncated: %q", out)
	}
}

func TestFormatReadOffsetLimitWindow(t *testing.T) {
	res := domain.ReadResult{Content: "a\nb\nc\nd\ne", SizeBytes: 9}
	out := formatRead("f.txt", res, 2, 2)
	if !strings.Contains(out, "lines 2-3 of 5") {
		t.Fatalf("wrong window header: %q", out)
	}
	if strings.Contains(out, "1→a") || strings.Contains(out, "4→d") {
		t.Fatalf("lines outside window leaked: %q", out)
	}
	if !strings.Contains(out, "[TRUNCATED — continue with offset=4]") {
		t.Fatalf("missing continue marker: %q", out)
	}
}

func TestFormatReadOffsetBeyondEnd(t *testing.T) {
	res := domain.ReadResult{Content: "a\nb", SizeBytes: 3}
	out := formatRead("f.txt", res, 10, 0)
	if !strings.Contains(out, "has 2 lines") || !strings.Contains(out, "offset 10") {
		t.Fatalf("wrong beyond-end message: %q", out)
	}
}

func TestFormatReadByteCapStopsEarly(t *testing.T) {
	// 100 linhas de ~1KB: estoura maxReadOutputBytes (48KB) antes da linha 100.
	line := strings.Repeat("x", 1024)
	content := strings.TrimRight(strings.Repeat(line+"\n", 100), "\n")
	res := domain.ReadResult{Content: content, SizeBytes: int64(len(content))}
	out := formatRead("big.txt", res, 1, 0)
	if len(out) > maxReadOutputBytes+1024 {
		t.Fatalf("output exceeds byte cap: %d bytes", len(out))
	}
	if !strings.Contains(out, "[TRUNCATED — continue with offset=") {
		t.Fatalf("missing continue marker: %q", out[:200])
	}
}

func TestFormatEditIsCompact(t *testing.T) {
	out := formatEdit("f.txt", domain.EditResult{Replacements: 2, BytesWritten: 10})
	if out != "f.txt (2 replacements)" {
		t.Fatalf("unexpected: %q", out)
	}
	out = formatEdit("f.txt", domain.EditResult{Replacements: 1, BytesWritten: 10})
	if out != "f.txt (1 replacement)" {
		t.Fatalf("unexpected: %q", out)
	}
}

func TestFormatReadContinueOffsetChains(t *testing.T) {
	res := domain.ReadResult{Content: "a\nb\nc\nd", SizeBytes: 7}
	first := formatRead("f.txt", res, 1, 2)
	if !strings.Contains(first, "continue with offset=3") {
		t.Fatalf("first page: %q", first)
	}
	second := formatRead("f.txt", res, 3, 2)
	if !strings.Contains(second, "lines 3-4 of 4") || strings.Contains(second, "TRUNCATED") {
		t.Fatalf("second page: %q", second)
	}
}
