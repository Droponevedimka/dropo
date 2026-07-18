//go:build windows

package main

import "testing"

func TestLoopbackProxyPortParsing(t *testing.T) {
	tests := []struct {
		value string
		want  int
	}{
		{"127.0.0.1:2088", 2088},
		{"http=127.0.0.1:2088;https=localhost:2088", 2088},
		{"http://127.0.0.1:7301", 7301},
		{"proxy.example:8080", 0},
		{"", 0},
	}
	for _, test := range tests {
		if got := loopbackProxyPort(test.value); got != test.want {
			t.Errorf("loopbackProxyPort(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}
