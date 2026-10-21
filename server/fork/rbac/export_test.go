package rbac

import "time"

type TOTPGuardForTest struct{ g *totpGuard }

func NewTOTPGuardForTest() *TOTPGuardForTest { return &TOTPGuardForTest{newTOTPGuard()} }

func (t *TOTPGuardForTest) Check(n string, seed []byte, code string, now time.Time) bool {
	return t.g.check(n, seed, code, now)
}

func (t *TOTPGuardForTest) Fail(n string, now time.Time) {
	t.g.fail(n, now)
}

func (t *TOTPGuardForTest) Locked(n string, now time.Time) bool {
	return t.g.locked(n, now)
}

func TOTPCodeForTest(seed []byte, step uint64) string { return totpCode(seed, step) }
