//go:build !windows

package main

func systemProxyURL() string {
	return ""
}
