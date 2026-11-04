//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func systemProxyURL() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()

	enable, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enable == 0 {
		return ""
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil {
		return ""
	}
	return normalizeProxyServer(server)
}

func normalizeProxyServer(server string) string {
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	if strings.Contains(server, "=") {
		var plain, secure, socks string
		for _, part := range strings.Split(server, ";") {
			name, val, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "https":
				secure = strings.TrimSpace(val)
			case "http":
				plain = strings.TrimSpace(val)
			case "socks":
				socks = strings.TrimSpace(val)
			}
		}
		switch {
		case secure != "":
			server = secure
		case plain != "":
			server = plain
		case socks != "":
			server = "socks5://" + socks
		default:
			return ""
		}
	}
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	return server
}
