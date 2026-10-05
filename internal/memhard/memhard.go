// Package memhard computes a function that needs a fixed amount of memory:
// the proof of work whose cost is memory as well as time.
//
// The usual proof of work (SHA-256) costs time only, and a graphics card or
// a rented server tries thousands of candidates side by side. Here every
// try needs some megabytes of its own, so a thousand tries side by side
// need a thousand times the memory.
//
// The function is scrypt (RFC 7914) with r = 8 and p = 1: nothing of our
// own invention, and checked against another implementation in the tests.
// A solution to the task is a number n for which
//
//	SHA-256(nonce + n)               starts with FilterBits zero bits, and
//	scrypt(nonce + n, Salt, N, 8, 1) starts with the task's zero bits.
//
// The first condition lets the server throw out numbers sent at random
// for the price of one hash. It is a small hurdle, not a defence: checking
// an answer that clears it costs one scrypt, a few milliseconds and
// megabytes. What bounds that cost is the caller's business (each task
// checked once, a budget of wrong answers per network; see
// internal/challenge) and the Verifier, which runs only a few checks at
// once.
package memhard

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"time"
)

// The parameters of the task.
const (
	// Salt separates this use of scrypt from any other.
	Salt = "xibalba/pow-memory/1"
	// FilterBits is how many zero bits SHA-256(nonce + n) must start with
	// before an answer is looked at any further.
	FilterBits = 12
	// MinDifficulty and MaxDifficulty bound the zero bits the scrypt
	// value must start with. Each bit doubles the tries.
	MinDifficulty = 1
	MaxDifficulty = 10

	r           = 8
	blockWords  = 32 * r // one block of 128·r bytes, as 32-bit words
	maxSolution = 15
)

// Sizes lists the amounts of memory one try may need, in MiB.
var Sizes = []int{1, 2, 4, 8, 16}

// ValidSize reports whether size is one of Sizes.
func ValidSize(size int) bool {
	for _, s := range Sizes {
		if s == size {
			return true
		}
	}
	return false
}

// Scratch is the memory one computation works in. It grows to what the
// largest computation needed and can be used again, one computation at a
// time.
type Scratch struct {
	v   []uint32
	x   [blockWords]uint32
	tmp [blockWords]uint32
}

// sum returns the first 32 bytes of scrypt(password, Salt, N, r = 8, p = 1)
// with N = size · 1024, which makes it need size MiB. size must be one of
// Sizes; the exported functions see to that. scratch may be nil.
func sum(password []byte, size int, scratch *Scratch) [32]byte {
	if scratch == nil {
		scratch = &Scratch{}
	}
	n := size * 1024
	if need := n * blockWords; cap(scratch.v) < need {
		scratch.v = make([]uint32, need)
	}
	v := scratch.v[:n*blockWords]

	// PBKDF2 with one round cannot fail for these arguments.
	b, _ := pbkdf2.Key(sha256.New, string(password), []byte(Salt), 1, blockWords*4)
	x, tmp := scratch.x[:], scratch.tmp[:]
	for i := range x {
		x[i] = binary.LittleEndian.Uint32(b[4*i:])
	}

	// ROMix: fill the memory, then read it back in an order that depends
	// on the data, so that none of it can be left out.
	for i := 0; i < n; i++ {
		copy(v[i*blockWords:], x)
		blockMix(x, tmp)
	}
	mask := uint32(n - 1)
	for i := 0; i < n; i++ {
		j := int(x[blockWords-16]&mask) * blockWords
		for k := range x {
			x[k] ^= v[j+k]
		}
		blockMix(x, tmp)
	}

	for i, word := range x {
		binary.LittleEndian.PutUint32(b[4*i:], word)
	}
	out, _ := pbkdf2.Key(sha256.New, string(password), b, 1, 32)
	var result [32]byte
	copy(result[:], out)
	return result
}

// blockMix is scrypt's BlockMix for r = 8: sixteen 64-byte pieces, each
// mixed with the one before, the even ones then the odd ones.
func blockMix(b, tmp []uint32) {
	var x [16]uint32
	copy(x[:], b[blockWords-16:])
	for i := 0; i < 2*r; i++ {
		piece := b[i*16 : i*16+16]
		for k := range x {
			x[k] ^= piece[k]
		}
		salsa8(&x)
		// Even pieces go to the first half, odd ones to the second.
		copy(tmp[(i/2+(i%2)*r)*16:], x[:])
	}
	copy(b, tmp)
}

// salsa8 is the Salsa20 core with 8 rounds.
func salsa8(b *[16]uint32) {
	x := *b
	for i := 0; i < 8; i += 2 {
		x[4] ^= bits.RotateLeft32(x[0]+x[12], 7)
		x[8] ^= bits.RotateLeft32(x[4]+x[0], 9)
		x[12] ^= bits.RotateLeft32(x[8]+x[4], 13)
		x[0] ^= bits.RotateLeft32(x[12]+x[8], 18)
		x[9] ^= bits.RotateLeft32(x[5]+x[1], 7)
		x[13] ^= bits.RotateLeft32(x[9]+x[5], 9)
		x[1] ^= bits.RotateLeft32(x[13]+x[9], 13)
		x[5] ^= bits.RotateLeft32(x[1]+x[13], 18)
		x[14] ^= bits.RotateLeft32(x[10]+x[6], 7)
		x[2] ^= bits.RotateLeft32(x[14]+x[10], 9)
		x[6] ^= bits.RotateLeft32(x[2]+x[14], 13)
		x[10] ^= bits.RotateLeft32(x[6]+x[2], 18)
		x[3] ^= bits.RotateLeft32(x[15]+x[11], 7)
		x[7] ^= bits.RotateLeft32(x[3]+x[15], 9)
		x[11] ^= bits.RotateLeft32(x[7]+x[3], 13)
		x[15] ^= bits.RotateLeft32(x[11]+x[7], 18)

		x[1] ^= bits.RotateLeft32(x[0]+x[3], 7)
		x[2] ^= bits.RotateLeft32(x[1]+x[0], 9)
		x[3] ^= bits.RotateLeft32(x[2]+x[1], 13)
		x[0] ^= bits.RotateLeft32(x[3]+x[2], 18)
		x[6] ^= bits.RotateLeft32(x[5]+x[4], 7)
		x[7] ^= bits.RotateLeft32(x[6]+x[5], 9)
		x[4] ^= bits.RotateLeft32(x[7]+x[6], 13)
		x[5] ^= bits.RotateLeft32(x[4]+x[7], 18)
		x[11] ^= bits.RotateLeft32(x[10]+x[9], 7)
		x[8] ^= bits.RotateLeft32(x[11]+x[10], 9)
		x[9] ^= bits.RotateLeft32(x[8]+x[11], 13)
		x[10] ^= bits.RotateLeft32(x[9]+x[8], 18)
		x[12] ^= bits.RotateLeft32(x[15]+x[14], 7)
		x[13] ^= bits.RotateLeft32(x[12]+x[15], 9)
		x[14] ^= bits.RotateLeft32(x[13]+x[12], 13)
		x[15] ^= bits.RotateLeft32(x[14]+x[13], 18)
	}
	for i := range b {
		b[i] += x[i]
	}
}

func leadingZeros(value [32]byte) int {
	return bits.LeadingZeros32(binary.BigEndian.Uint32(value[:4]))
}

// plain reports whether nonce and solution have the form a task gives them.
func plain(nonce, solution string) bool {
	if nonce == "" || len(nonce) > 64 || solution == "" || len(solution) > maxSolution {
		return false
	}
	for i := 0; i < len(solution); i++ {
		if solution[i] < '0' || solution[i] > '9' {
			return false
		}
	}
	return true
}

// Filter reports whether an answer passes the cheap first condition. It
// costs one SHA-256.
func Filter(nonce, solution string) bool {
	if !plain(nonce, solution) {
		return false
	}
	return leadingZeros(sha256.Sum256([]byte(nonce+solution))) >= FilterBits
}

// Solves reports whether solution answers the task: both conditions. It
// costs size MiB and a few milliseconds if the first condition holds, and
// one SHA-256 if not.
func Solves(nonce, solution string, size, difficulty int, scratch *Scratch) bool {
	if !ValidSize(size) || difficulty < MinDifficulty || difficulty > MaxDifficulty || !Filter(nonce, solution) {
		return false
	}
	return leadingZeros(sum([]byte(nonce+solution), size, scratch)) >= difficulty
}

// Verifier checks answers, a few at a time, and keeps the memory for it.
type Verifier struct {
	slots chan *Scratch
	wait  time.Duration
}

// NewVerifier returns a Verifier that runs at most parallel checks at once
// and lets an answer wait for its turn for at most wait. Its memory grows
// to parallel times the largest size it was asked about, and stays.
func NewVerifier(parallel int, wait time.Duration) *Verifier {
	if parallel < 1 {
		parallel = 1
	}
	v := &Verifier{slots: make(chan *Scratch, parallel), wait: wait}
	for i := 0; i < parallel; i++ {
		v.slots <- &Scratch{}
	}
	return v
}

// Solves is the function Solves with a place in line. busy is true if the
// answer could not be checked because too many others were being checked;
// it is then neither right nor wrong. An answer that fails the cheap first
// condition never waits.
func (v *Verifier) Solves(ctx context.Context, nonce, solution string, size, difficulty int) (ok, busy bool) {
	if !ValidSize(size) || difficulty < MinDifficulty || difficulty > MaxDifficulty || !Filter(nonce, solution) {
		return false, false
	}
	timer := time.NewTimer(v.wait)
	defer timer.Stop()
	select {
	case scratch := <-v.slots:
		defer func() { v.slots <- scratch }()
		return leadingZeros(sum([]byte(nonce+solution), size, scratch)) >= difficulty, false
	case <-timer.C:
		return false, true
	case <-ctx.Done():
		return false, true
	}
}

// Hold takes one place in line for as long as fn runs. It is for tests of
// what happens when every place is taken.
func (v *Verifier) Hold(fn func()) {
	scratch := <-v.slots
	defer func() { v.slots <- scratch }()
	fn()
}
