package admin

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The password is never stored. What is stored is a line
//
//	pbkdf2-sha256$<rounds>$<salt>$<key>
//
// with salt and key in base64. PBKDF2 is in the standard library; the
// number of rounds makes guessing slow.
const (
	hashScheme = "pbkdf2-sha256"
	// hashRounds is used for new hashes. One check takes about a tenth of
	// a second on a server and about a second on a Raspberry Pi.
	hashRounds    = 600000
	minHashRounds = 100000
	maxHashRounds = 10000000
	saltBytes     = 16
	keyBytes      = 32
	// MinPasswordLength is the shortest password HashPassword accepts.
	MinPasswordLength = 12
	// maxPasswordLength bounds the work one login attempt can cause.
	maxPasswordLength = 1024
)

// A Hash is a stored password in its parsed form.
type Hash struct {
	rounds int
	salt   []byte
	key    []byte
}

// HashPassword returns the line to store for a password.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", fmt.Errorf("the password is shorter than %d characters", MinPasswordLength)
	}
	if len(password) > maxPasswordLength {
		return "", fmt.Errorf("the password is longer than %d bytes", maxPasswordLength)
	}
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashRounds, keyBytes)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, hashRounds, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// ParseHash reads a stored line. The error never repeats the line.
func ParseHash(line string) (Hash, error) {
	parts := strings.Split(strings.TrimSpace(line), "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return Hash{}, errors.New("it does not start with " + hashScheme)
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds < minHashRounds || rounds > maxHashRounds {
		return Hash{}, errors.New("the number of rounds is out of range")
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	key, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(salt) > 64 || len(key) != keyBytes {
		return Hash{}, errors.New("salt or key are damaged")
	}
	return Hash{rounds: rounds, salt: salt, key: key}, nil
}

// Matches reports whether password is the stored one. It takes the same
// time for every wrong password.
func (h Hash) Matches(password string) bool {
	if h.rounds == 0 || len(password) > maxPasswordLength {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, h.salt, h.rounds, keyBytes)
	return err == nil && subtle.ConstantTimeCompare(key, h.key) == 1
}
