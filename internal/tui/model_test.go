package tui

import "testing"

func TestExtractPrimaryToolArg(t *testing.T) {
	tests := []struct {
		name string
		call toolCall
		want string
	}{
		{
			name: "uses prioritized json key",
			call: toolCall{Args: `{"path":"internal/tui/model.go","query":"ignored"}`},
			want: "internal/tui/model.go",
		},
		{
			name: "falls back to short raw args",
			call: toolCall{Args: "raw-short-arg"},
			want: "raw-short-arg",
		},
		{
			name: "ignores long raw args fallback",
			call: toolCall{Args: "this-is-a-very-long-raw-argument-string-that-should-not-be-used-as-a-fallback"},
			want: "",
		},
		{
			name: "falls back to short raw json when no key",
			call: toolCall{Args: `{"unknown":"value"}`},
			want: `{"unknown":"value"}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := extractPrimaryToolArg(tt.call)
			if got != tt.want {
				t.Fatalf("extractPrimaryToolArg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatToolResultSummary(t *testing.T) {
	longResult := ""
	for i := 0; i < 201; i++ {
		longResult += "a"
	}

	tests := []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "reports char count for long result",
			result: longResult,
			want:   "(201 chars)",
		},
		{
			name:   "reports line count for multiline result",
			result: "line1\nline2\nline3",
			want:   "(3 lines)",
		},
		{
			name:   "empty for short single-line result",
			result: "ok",
			want:   "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolResultSummary(tt.result)
			if got != tt.want {
				t.Fatalf("formatToolResultSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{in: 0, want: "0"},
		{in: 7, want: "7"},
		{in: 123, want: "123"},
		{in: 1234, want: "1,234"},
		{in: 1234567, want: "1,234,567"},
	}

	for _, tt := range tests {
		got := formatTokens(tt.in)
		if got != tt.want {
			t.Fatalf("formatTokens(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
