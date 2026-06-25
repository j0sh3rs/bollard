package log_test

import (
	"bytes"
	"strings"
	"testing"

	bollardlog "github.com/j0sh3rs/bollard/internal/log"
)

func TestNewWithWriter(t *testing.T) {
	tests := []struct {
		name      string
		format    string
		level     string
		wantErr   bool
		wantNil   bool
		checkOut  bool
		outSubstr string
	}{
		{
			name:     "logfmt with info level",
			format:   "logfmt",
			level:    "info",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
		{
			name:      "json with info level",
			format:    "json",
			level:     "info",
			wantErr:   false,
			wantNil:   false,
			checkOut:  true,
			outSubstr: `"msg":"test message"`,
		},
		{
			name:     "json with debug level",
			format:   "json",
			level:    "debug",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
		{
			name:     "logfmt with warn level",
			format:   "logfmt",
			level:    "warn",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
		{
			name:     "json with error level",
			format:   "json",
			level:    "error",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
		{
			name:     "invalid format",
			format:   "invalid",
			level:    "info",
			wantErr:  true,
			wantNil:  true,
			checkOut: false,
		},
		{
			name:     "invalid level",
			format:   "json",
			level:    "verbose",
			wantErr:  true,
			wantNil:  true,
			checkOut: false,
		},
		{
			name:     "uppercase level INFO",
			format:   "json",
			level:    "INFO",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
		{
			name:     "uppercase level WARN",
			format:   "logfmt",
			level:    "WARN",
			wantErr:  false,
			wantNil:  false,
			checkOut: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := bollardlog.NewWithWriter(tt.format, tt.level, &buf)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, want error %v", err != nil, tt.wantErr)
			}
			if (logger == nil) != tt.wantNil {
				t.Errorf("got logger nil %v, want nil %v", logger == nil, tt.wantNil)
			}
			if tt.checkOut && logger != nil {
				logger.Info("test message", "key", "value")
				out := buf.String()
				if !strings.Contains(out, tt.outSubstr) {
					t.Errorf("expected output to contain %q, got: %s", tt.outSubstr, out)
				}
			}
		})
	}
}

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		level   string
		wantErr bool
	}{
		{
			name:    "logfmt with info",
			format:  "logfmt",
			level:   "info",
			wantErr: false,
		},
		{
			name:    "json with debug",
			format:  "json",
			level:   "debug",
			wantErr: false,
		},
		{
			name:    "invalid format",
			format:  "text",
			level:   "info",
			wantErr: true,
		},
		{
			name:    "invalid level",
			format:  "json",
			level:   "trace",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, err := bollardlog.New(tt.format, tt.level)
			if (err != nil) != tt.wantErr {
				t.Errorf("got error %v, want error %v", err != nil, tt.wantErr)
			}
			if !tt.wantErr && logger == nil {
				t.Error("expected non-nil logger on success")
			}
		})
	}
}
