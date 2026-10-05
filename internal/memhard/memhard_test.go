package memhard

import (
	"context"
	"encoding/hex"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The values come from Python's hashlib.scrypt (OpenSSL), with the salt of
// this package, N = size · 1024, r = 8, p = 1, 32 bytes.
func TestScryptAgreesWithAnotherImplementation(t *testing.T) {
	cases := []struct {
		password string
		size     int
		want     string
	}{
		{"", 1, "4379fdf4f3dd543c450ebc7ebcd794cc7bea080491223faf7ca94361e8f2aeac"},
		{"abc123", 1, "511b406d4b1a1e720b6a7e6585392b0809e4c50797363765fe13736801439eb9"},
		{"0123456789abcdef0123456789abcdef42", 4, "194d171529371e80c453922fe42a1a1ee87a7eaabc5a1d7cd62c544be3777352"},
		{"pleaseletmein", 16, "33ddc48014b0d1ff9067f68a2433f31e7f660d9f83fadef3c814e8dcd8128e01"},
	}
	scratch := &Scratch{}
	for _, c := range cases {
		value := sum([]byte(c.password), c.size, nil)
		if got := hex.EncodeToString(value[:]); got != c.want {
			t.Errorf("scrypt(%q, %d) = %s, want %s", c.password, c.size, got, c.want)
		}
		// The same with memory that was used before, larger and smaller.
		for _, first := range []int{16, 1} {
			sum([]byte("something else"), first, scratch)
			value = sum([]byte(c.password), c.size, scratch)
			if got := hex.EncodeToString(value[:]); got != c.want {
				t.Errorf("with used memory: scrypt(%q, %d) = %s", c.password, c.size, got)
			}
		}
	}
}

// solve finds an answer the way a browser does.
func solve(nonce string, size, difficulty int) string {
	scratch := &Scratch{}
	for n := 0; ; n++ {
		s := strconv.Itoa(n)
		if Filter(nonce, s) && leadingZeros(sum([]byte(nonce+s), size, scratch)) >= difficulty {
			return s
		}
	}
}

func TestSolves(t *testing.T) {
	const nonce = "5f1d3c9a7b2e4f60"
	answer := solve(nonce, 1, 2)
	if !Solves(nonce, answer, 1, 2, nil) {
		t.Fatalf("the answer %s is not accepted", answer)
	}
	bad := []struct {
		name, nonce, solution string
		size, difficulty      int
	}{
		{"another task", "0000000000000000", answer, 1, 2},
		{"more memory asked", nonce, answer, 2, 2},
		{"empty", nonce, "", 1, 2},
		{"not a number", nonce, "12a", 1, 2},
		{"negative", nonce, "-1", 1, 2},
		{"padded", nonce, " " + answer, 1, 2},
		{"too long", nonce, "1234567890123456", 1, 2},
		{"no nonce", "", answer, 1, 2},
		{"size not offered", nonce, answer, 3, 2},
		{"size zero", nonce, answer, 0, 2},
		{"huge size", nonce, answer, 1 << 20, 2},
		{"difficulty zero", nonce, answer, 1, 0},
		{"difficulty too high", nonce, answer, 1, 11},
	}
	for _, c := range bad {
		if Solves(c.nonce, c.solution, c.size, c.difficulty, nil) {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// An answer that passes the first condition but not the second.
	for n := 0; ; n++ {
		s := strconv.Itoa(n)
		if Filter(nonce, s) && leadingZeros(sum([]byte(nonce+s), 1, nil)) < 10 {
			if Solves(nonce, s, 1, 10, nil) {
				t.Errorf("%s passes only the first condition and is accepted", s)
			}
			break
		}
	}
}

func TestTheCheapConditionComesFirst(t *testing.T) {
	const nonce = "5f1d3c9a7b2e4f60"
	wrong := "1"
	for Filter(nonce, wrong) {
		wrong += "1"
	}
	began := time.Now()
	for i := 0; i < 2000; i++ {
		if Solves(nonce, wrong, 16, 1, nil) {
			t.Fatal("accepted")
		}
	}
	// 2000 scrypts of 16 MiB would take minutes.
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("answers that fail the first condition cost %s for 2000", took)
	}
}

func TestVerifierRunsFewAtOnceAndSaysWhenBusy(t *testing.T) {
	const nonce = "5f1d3c9a7b2e4f60"
	answer := solve(nonce, 1, 1)
	v := NewVerifier(2, 5*time.Second)
	if ok, busy := v.Solves(context.Background(), nonce, answer, 1, 1); !ok || busy {
		t.Fatalf("ok %v, busy %v", ok, busy)
	}
	if ok, busy := v.Solves(context.Background(), nonce, "x", 1, 1); ok || busy {
		t.Errorf("nonsense: ok %v, busy %v", ok, busy)
	}

	// Many at once: all are answered, never more than two at a time.
	var wg sync.WaitGroup
	var right atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := v.Solves(context.Background(), nonce, answer, 1, 1); ok {
				right.Add(1)
			}
		}()
	}
	wg.Wait()
	if right.Load() != 20 || len(v.slots) != 2 {
		t.Errorf("%d of 20 answered, %d places free afterwards", right.Load(), len(v.slots))
	}

	// With every place taken, an answer waits its time and is then told so.
	none := NewVerifier(1, 30*time.Millisecond)
	held := <-none.slots
	began := time.Now()
	ok, busy := none.Solves(context.Background(), nonce, answer, 1, 1)
	if ok || !busy || time.Since(began) > 2*time.Second {
		t.Errorf("ok %v, busy %v after %s", ok, busy, time.Since(began))
	}
	// An answer that fails the first condition is refused at once, busy or not.
	if ok, busy := none.Solves(context.Background(), nonce, "x", 1, 1); ok || busy {
		t.Errorf("nonsense while busy: ok %v, busy %v", ok, busy)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, busy := none.Solves(ctx, nonce, answer, 1, 1); ok || !busy {
		t.Errorf("request gone: ok %v, busy %v", ok, busy)
	}
	none.slots <- held
}

func BenchmarkSum(b *testing.B) {
	for _, size := range Sizes {
		b.Run(strconv.Itoa(size)+"MiB", func(b *testing.B) {
			scratch := &Scratch{}
			password := []byte("5f1d3c9a7b2e4f60123456")
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sum(password, size, scratch)
			}
		})
	}
}
