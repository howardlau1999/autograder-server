package main

import "testing"

func TestOriginPolicyCrossOrigin(t *testing.T) {
	prod := newOriginPolicy([]string{"https://app.example.com"}, false)
	if !prod.allowCrossOrigin("https://app.example.com") {
		t.Error("whitelisted origin must be allowed")
	}
	if prod.allowCrossOrigin("https://evil.example.com") {
		t.Error("unlisted origin must be rejected in production")
	}
	if prod.allowCrossOrigin("") {
		t.Error("empty origin is not on the whitelist")
	}
	dev := newOriginPolicy(nil, true)
	if !dev.allowCrossOrigin("https://anything.example.com") {
		t.Error("development mode reflects any origin")
	}
}
