package hotswap

import (
	"encoding/base64"
	"testing"
)

func TestParseReady(t *testing.T) {
	tests := []struct {
		line    string
		wantPID int
		wantErr bool
	}{
		{"READY pid=1234", 1234, false},
		{"READY pid=1", 1, false},
		{"READY pid=99999", 99999, false},
		{"READY", 0, true},       // Missing payload
		{"READY pid=abc", 0, true}, // Non-numeric PID
		{"BYE", 0, true},         // Wrong message type
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			pid, err := ParseReady(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseReady(%q) error = %v, wantErr %v", tt.line, err, tt.wantErr)
				return
			}
			if !tt.wantErr && pid != tt.wantPID {
				t.Errorf("ParseReady(%q) pid = %d, want %d", tt.line, pid, tt.wantPID)
			}
		})
	}
}

func TestParseDUP(t *testing.T) {
	// Create a test blob
	testBlob := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	encoded := base64.StdEncoding.EncodeToString(testBlob)

	tests := []struct {
		line     string
		wantBlob []byte
		wantErr  bool
	}{
		{"DUP " + encoded, testBlob, false},
		{"DUP " + base64.StdEncoding.EncodeToString([]byte("hello")), []byte("hello"), false},
		{"DUP ", nil, false},           // Empty payload = empty blob (valid)
		{"DUP invalid-base64!!!", nil, true}, // Invalid base64
		{"READY pid=1234", nil, true}, // Wrong message type
	}

	for _, tt := range tests {
		t.Run(func() string { s := tt.line; if len(s) > 20 { return s[:20] + "..." }; return s }(), func(t *testing.T) {
			blob, err := ParseDUP(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDUP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(blob) != len(tt.wantBlob) {
				t.Errorf("ParseDUP() blob len = %d, want %d", len(blob), len(tt.wantBlob))
			}
		})
	}
}

func TestParseGOT(t *testing.T) {
	tests := []struct {
		line     string
		wantOK   bool
		wantErr  string
		wantErrs bool
	}{
		{"GOT ok", true, "", false},
		{"GOT err=socket closed", false, "socket closed", false},
		{"GOT err=", false, "", false},
		{"GOT ", false, "", false}, // Empty payload = empty error
		{"READY pid=1234", false, "", true}, // Wrong type
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			ok, errMsg, err := ParseGOT(tt.line)
			if (err != nil) != tt.wantErrs {
				t.Errorf("ParseGOT(%q) error = %v, wantErrs %v", tt.line, err, tt.wantErrs)
				return
			}
			if !tt.wantErrs {
				if ok != tt.wantOK {
					t.Errorf("ParseGOT(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
				}
				if errMsg != tt.wantErr {
					t.Errorf("ParseGOT(%q) errMsg = %q, want %q", tt.line, errMsg, tt.wantErr)
				}
			}
		})
	}
}

func TestParseACK(t *testing.T) {
	tests := []struct {
		line    string
		wantErr bool
	}{
		{"ACK", false},
		{"GOT ok", true},  // Wrong type
		{"BYE", true},     // Wrong type
		{"", true},        // Empty
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			err := ParseACK(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseACK(%q) error = %v, wantErr %v", tt.line, err, tt.wantErr)
			}
		})
	}
}

func TestParseMessage(t *testing.T) {
	tests := []struct {
		line     string
		wantType string
		wantPay  string
	}{
		{"READY pid=1234", "READY", "pid=1234"},
		{"DUP abc123", "DUP", "abc123"},
		{"GOT ok", "GOT", "ok"},
		{"ACK", "ACK", ""},
		{"BYE", "BYE", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			msgType, payload, err := ParseMessage(tt.line)
			if err != nil {
				t.Fatalf("ParseMessage(%q) error: %v", tt.line, err)
			}
			if msgType != tt.wantType {
				t.Errorf("ParseMessage(%q) type = %q, want %q", tt.line, msgType, tt.wantType)
			}
			if payload != tt.wantPay {
				t.Errorf("ParseMessage(%q) payload = %q, want %q", tt.line, payload, tt.wantPay)
			}
		})
	}
}
