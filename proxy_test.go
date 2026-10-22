package main

import "testing"

func TestParseProxyServer(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:10809": "http://127.0.0.1:10809",
		"http=127.0.0.1:10809;https=127.0.0.1:10809": "http://127.0.0.1:10809",
		"socks=127.0.0.1:10808":                      "socks5://127.0.0.1:10808",
		"http=127.0.0.1:2080;socks=127.0.0.1:2081":   "http://127.0.0.1:2080",
		"":                         "",
		"socks5://127.0.0.1:10808": "socks5://127.0.0.1:10808",
	} {
		if got := parseProxyServer(in); got != want {
			t.Errorf("parseProxyServer(%q) = %q, want %q", in, got, want)
		}
	}
}
