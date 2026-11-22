package password_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pandokos/pandokos/internal/password"
	"github.com/pandokos/pandokos/internal/secret"
)

// floor is the cheapest set of parameters the configuration accepts (sprint 4.2, section 5.4).
var floor = password.Params{MemoryKiB: 19456, Iterations: 2, Parallelism: 1}

func hasher(t testing.TB, p password.Params, max int) *password.Hasher {
	t.Helper()
	h, err := password.New(p, max)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// random returns a password made when the test runs.
func random(t testing.TB) secret.Secret {
	t.Helper()
	pw, err := password.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return pw
}

var rePHC = regexp.MustCompile(`^\$argon2id\$v=19\$m=(\d+),t=(\d+),p=(\d+)\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)$`)

// TestHashVerify is T-766.
func TestHashVerify(t *testing.T) {
	ctx := context.Background()
	h := hasher(t, floor, 4)
	pw := random(t)
	phc, err := h.Hash(ctx, pw)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	m := rePHC.FindStringSubmatch(phc)
	if m == nil {
		t.Fatalf("not a PHC string of Argon2id version 19 in unpadded base64: %q", phc)
	}
	if m[1] != "19456" || m[2] != "2" || m[3] != "1" {
		t.Errorf("parameters %s %s %s, want 19456 2 1", m[1], m[2], m[3])
	}
	if len(m[4]) != 22 { // 16 bytes in unpadded base64
		t.Errorf("salt of %d characters, want 22 (16 bytes)", len(m[4]))
	}
	if len(m[5]) != 43 { // 32 bytes in unpadded base64
		t.Errorf("output of %d characters, want 43 (32 bytes)", len(m[5]))
	}
	if strings.Contains(phc, "=") {
		t.Error("padded base64")
	}
	if strings.Contains(phc, pw.Reveal()) {
		t.Error("the hash contains the password")
	}

	ok, outdated, err := h.Verify(ctx, phc, pw)
	if err != nil || !ok || outdated {
		t.Errorf("Verify of the right password: ok=%v outdated=%v err=%v", ok, outdated, err)
	}
	ok, _, err = h.Verify(ctx, phc, secret.New(pw.Reveal()+"x"))
	if err != nil || ok {
		t.Errorf("Verify of another password: ok=%v err=%v", ok, err)
	}
	ok, _, err = h.Verify(ctx, phc, secret.New(""))
	if err != nil || ok {
		t.Errorf("Verify of an empty password: ok=%v err=%v", ok, err)
	}

	again, err := h.Hash(ctx, pw)
	if err != nil {
		t.Fatal(err)
	}
	if again == phc {
		t.Error("two hashes of one password are equal: the salt is not random")
	}

	// Outdated: any one parameter differs.
	for name, p := range map[string]password.Params{
		"memory":      {MemoryKiB: 19457, Iterations: 2, Parallelism: 1},
		"iterations":  {MemoryKiB: 19456, Iterations: 3, Parallelism: 1},
		"parallelism": {MemoryKiB: 19456, Iterations: 2, Parallelism: 2},
	} {
		ok, outdated, err := hasher(t, p, 4).Verify(ctx, phc, pw)
		if err != nil || !ok || !outdated {
			t.Errorf("%s changed: ok=%v outdated=%v err=%v, want true true", name, ok, outdated, err)
		}
	}
}

// TestKnownAnswer is the other half of T-766: a hash made by the reference implementation
// (phc-winner-argon2, test.c: Argon2id, version 19, t=2, m=65536, p=1, password "password", salt
// "somesalt") is verified, and a changed password is not.
func TestKnownAnswer(t *testing.T) {
	const phc = "$argon2id$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$CTFhFdXPJO1aFaMaO6Mm5c8y7cJHAph8ArZWb2GRPPc"
	h := hasher(t, password.Params{MemoryKiB: 65536, Iterations: 2, Parallelism: 1}, 2)
	ok, outdated, err := h.Verify(context.Background(), phc, secret.New("password"))
	if err != nil || !ok || outdated {
		t.Fatalf("the reference vector: ok=%v outdated=%v err=%v", ok, outdated, err)
	}
	ok, _, err = h.Verify(context.Background(), phc, secret.New("Password"))
	if err != nil || ok {
		t.Errorf("another password verified against the reference vector: ok=%v err=%v", ok, err)
	}
}

// TestParsePHC is T-767: a hash that is not exactly the format is refused without computing.
func TestParsePHC(t *testing.T) {
	ctx := context.Background()
	h := hasher(t, floor, 4)
	salt, out := strings.Repeat("A", 22), strings.Repeat("a", 43)
	good := "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + out
	cases := map[string]string{
		"empty":                "",
		"only a dollar":        "$",
		"argon2i":              "$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + out,
		"argon2d":              "$argon2d$v=19$m=19456,t=2,p=1$" + salt + "$" + out,
		"bcrypt":               "$2a$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234",
		"version 16":           "$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + out,
		"no version":           "$argon2id$m=19456,t=2,p=1$" + salt + "$" + out,
		"order of parameters":  "$argon2id$v=19$t=2,m=19456,p=1$" + salt + "$" + out,
		"missing parameter":    "$argon2id$v=19$m=19456,t=2$" + salt + "$" + out,
		"missing hash":         "$argon2id$v=19$m=19456,t=2,p=1$" + salt,
		"missing salt":         "$argon2id$v=19$m=19456,t=2,p=1$$" + out,
		"padded salt":          "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "==$" + out,
		"padded hash":          "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + out + "=",
		"url alphabet":         "$argon2id$v=19$m=19456,t=2,p=1$" + strings.Repeat("-", 22) + "$" + out,
		"leading space":        " " + good,
		"trailing newline":     good + "\n",
		"memory over bound":    "$argon2id$v=19$m=1048577,t=2,p=1$" + salt + "$" + out,
		"memory huge":          "$argon2id$v=19$m=4294967295,t=2,p=1$" + salt + "$" + out,
		"iterations over":      "$argon2id$v=19$m=19456,t=11,p=1$" + salt + "$" + out,
		"parallelism over":     "$argon2id$v=19$m=19456,t=2,p=17$" + salt + "$" + out,
		"zero memory":          "$argon2id$v=19$m=0,t=2,p=1$" + salt + "$" + out,
		"zero iterations":      "$argon2id$v=19$m=19456,t=0,p=1$" + salt + "$" + out,
		"zero parallelism":     "$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + out,
		"negative":             "$argon2id$v=19$m=-1,t=2,p=1$" + salt + "$" + out,
		"extra segment":        good + "$x",
		"number overflows 64b": "$argon2id$v=19$m=99999999999999999999,t=2,p=1$" + salt + "$" + out,
	}
	for name, phc := range cases {
		before := h.Computations()
		ok, _, err := h.Verify(ctx, phc, secret.New("whatever password"))
		if ok {
			t.Errorf("%s: verified", name)
		}
		if err == nil {
			t.Errorf("%s: no error for a malformed hash", name)
		}
		if got := h.Computations(); got != before {
			t.Errorf("%s: %d computations for a hash that is refused", name, got-before)
		}
	}
	if ok, _, err := h.Verify(ctx, good, secret.New("whatever password")); err != nil || ok {
		t.Errorf("a well-formed hash of another password: ok=%v err=%v", ok, err)
	}
}

// FuzzParsePHC is the fuzz target of T-767: no input panics, and an input over the bounds never
// causes a computation.
func FuzzParsePHC(f *testing.F) {
	salt, out := strings.Repeat("A", 22), strings.Repeat("a", 43)
	for _, s := range []string{
		"", "$", "$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + out,
		"$argon2id$v=19$m=4294967295,t=4294967295,p=255$" + salt + "$" + out,
		"$argon2id$v=19$m=1048577,t=2,p=1$" + salt + "$" + out,
		"$argon2id$v=19$m=19456,t=2,p=1$$", "$$$$$$$", "$argon2id$v=19$m=,t=,p=$a$b",
	} {
		f.Add(s)
	}
	h, err := password.New(password.Params{MemoryKiB: 19456, Iterations: 2, Parallelism: 1}, 1)
	if err != nil {
		f.Fatalf("password.New: %v", err)
	}
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=(\d{1,8}),t=(\d{1,3}),p=(\d{1,3})\$`)
	pw := secret.New("a password for the fuzzer")
	f.Fuzz(func(t *testing.T, phc string) {
		if len(phc) > 300 {
			return
		}
		over := true
		if m := re.FindStringSubmatch(phc); m != nil {
			over = atoi(m[1]) > 1048576 || atoi(m[2]) > 10 || atoi(m[3]) > 16
		}
		before := h.Computations()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = h.Verify(ctx, phc, pw)
		if over && h.Computations() != before {
			t.Fatalf("a computation for a hash over the bounds: %q", phc)
		}
	})
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// TestPolicy is T-768.
func TestPolicy(t *testing.T) {
	const user = "ada.lovelace"
	check := func(pw string, min int) string { return password.Check(secret.New(pw), user, min) }

	// Length in characters at each boundary.
	for _, min := range []int{8, 12, 128} {
		if v := check(strings.Repeat("a", min-1), min); v != "must have at least "+itoa(min)+" characters" {
			t.Errorf("%d characters with a minimum of %d: %q", min-1, min, v)
		}
		if v := check(strings.Repeat("a", min), min); v != "" {
			t.Errorf("%d characters with a minimum of %d: %q", min, min, v)
		}
	}
	if v := check(strings.Repeat("a", 256), 12); v != "" {
		t.Errorf("256 characters: %q", v)
	}
	if v := check(strings.Repeat("a", 257), 12); v != "must have at most 256 characters" {
		t.Errorf("257 characters: %q", v)
	}
	if v := check("", 12); v != "must have at least 12 characters" {
		t.Errorf("empty: %q", v)
	}

	// Characters, not bytes: eleven and twelve of a three-byte and a four-byte character.
	for _, r := range []string{"é", "€", "😀"} {
		if v := check(strings.Repeat(r, 11), 12); v == "" {
			t.Errorf("eleven of %q accepted", r)
		}
		if v := check(strings.Repeat(r, 12), 12); v != "" {
			t.Errorf("twelve of %q: %q", r, v)
		}
	}
	four := strings.Repeat("😀", 256) // 1024 bytes
	if len(four) != 1024 || utf8.RuneCountInString(four) != 256 {
		t.Fatal("test data")
	}
	if v := check(four, 12); v != "" {
		t.Errorf("256 characters of 1024 bytes: %q", v)
	}
	if v := check(four+"a", 12); v == "" {
		t.Error("1025 bytes accepted")
	}

	// The username, in any letter case, and only the whole of it.
	for _, pw := range []string{user, "ADA.LOVELACE", "Ada.Lovelace"} {
		if v := password.Check(secret.New(pw), user, 8); v != "must not be the username" {
			t.Errorf("%q: %q", pw, v)
		}
	}
	if v := password.Check(secret.New(user+"-1"), user, 8); v != "" {
		t.Errorf("the username with a suffix: %q", v)
	}
	// The order of the rules: too short first.
	if v := password.Check(secret.New("ada"), "ada", 12); v != "must have at least 12 characters" {
		t.Errorf("short and the username: %q", v)
	}

	// No composition rule, any Unicode.
	for _, pw := range []string{"aaaaaaaaaaaa", "123456789012", "            ", "pässwörd-日本語-ok"} {
		if v := check(pw, 12); v != "" {
			t.Errorf("%q: %q", pw, v)
		}
	}

	// Nothing is truncated: two passwords equal in their first 72 bytes are different passwords.
	h := hasher(t, floor, 4)
	prefix := strings.Repeat("x", 72)
	a, b := secret.New(prefix+"-one"), secret.New(prefix+"-two")
	phc, err := h.Hash(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, err := h.Verify(context.Background(), phc, a); err != nil || !ok {
		t.Errorf("the password does not verify: %v %v", ok, err)
	}
	if ok, _, err := h.Verify(context.Background(), phc, b); err != nil || ok {
		t.Errorf("a password equal in its first 72 bytes verifies: %v %v", ok, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestGenerate is T-769.
func TestGenerate(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{24}$`)
	seen := map[string]bool{}
	used := map[rune]bool{}
	for i := 0; i < 10000; i++ {
		pw, err := password.Generate()
		if err != nil {
			t.Fatal(err)
		}
		v := pw.Reveal()
		if !re.MatchString(v) {
			t.Fatalf("a generated password is not 24 characters of a-z2-7: %q", v)
		}
		if seen[v] {
			t.Fatalf("the password %q was generated twice", v)
		}
		seen[v] = true
		for _, r := range v {
			used[r] = true
		}
		if v := password.Check(pw, "someone", 12); v != "" {
			t.Fatalf("a generated password breaks the policy: %s", v)
		}
	}
	if len(used) != 32 {
		t.Errorf("%d of the 32 characters of the alphabet were ever used", len(used))
	}
}

// TestConcurrency is T-770.
func TestConcurrency(t *testing.T) {
	// A cost that makes one computation last long enough to be seen.
	slow := password.Params{MemoryKiB: 262144, Iterations: 4, Parallelism: 1}
	h := hasher(t, slow, 2)
	pw := secret.New("a password for the concurrency test")

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Hash(context.Background(), pw); err != nil {
				t.Errorf("Hash: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // both slots are taken

	// A third computation waits for a slot; its context ends first: ErrBusy and no computation.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := h.Hash(ctx, pw)
	if !errors.Is(err, password.ErrBusy) {
		t.Errorf("a third computation with the two slots taken: %v, want ErrBusy", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, _, err := h.Verify(cancelled, "$argon2id$v=19$m=19456,t=2,p=1$"+strings.Repeat("A", 22)+"$"+strings.Repeat("a", 43), pw); !errors.Is(err, password.ErrBusy) {
		t.Errorf("a cancelled context: %v, want ErrBusy", err)
	}
	wg.Wait()
	if n := h.Computations(); n != 2 {
		t.Errorf("%d computations, want 2: a refused one computed", n)
	}

	// With a free slot a waiting caller proceeds.
	if _, err := h.Hash(context.Background(), pw); err != nil {
		t.Errorf("Hash with free slots: %v", err)
	}

	// Dummy costs one computation with the configured parameters, and returns no result.
	d := hasher(t, floor, 2)
	before := d.Computations()
	if err := d.Dummy(context.Background(), pw); err != nil {
		t.Fatalf("Dummy: %v", err)
	}
	if n := d.Computations() - before; n != 1 {
		t.Errorf("Dummy made %d computations, want 1", n)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := d.Dummy(ctx2, pw); !errors.Is(err, password.ErrBusy) {
		t.Errorf("Dummy with a cancelled context: %v, want ErrBusy", err)
	}
}

// TestNewRefuses checks the arguments of New: a limit below one and parameters below the floor
// of the configuration are not a hasher.
func TestNewRefuses(t *testing.T) {
	hasher(t, floor, 1) // the same arguments, valid, make a hasher
	if _, err := password.New(floor, 0); err == nil {
		t.Error("New with a limit of 0")
	}
	if _, err := password.New(password.Params{MemoryKiB: 19456, Iterations: 2, Parallelism: 0}, 1); err == nil {
		t.Error("New with a parallelism of 0")
	}
	if _, err := password.New(password.Params{MemoryKiB: 1 << 30, Iterations: 2, Parallelism: 1}, 1); err == nil {
		t.Error("New with a memory over the bound")
	}
}
