package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"regexp"
)

// secretPattern is the character set Telegram allows for webhook secret tokens. The kitchen
// display token uses the same rule so it is also a valid WebSocket subprotocol token.
var secretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

// secureEqual compares two secrets in constant time. Both sides are hashed first, so the running
// time does not depend on the length of either input. An empty expected value never matches.
func secureEqual(got, want string) bool {
	if want == "" {
		return false
	}
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func validateSecret(name, value string) error {
	if !secretPattern.MatchString(value) {
		return fmt.Errorf("%s must be 32 to 256 characters from A-Z, a-z, 0-9, underscore and hyphen", name)
	}
	return nil
}
