package admin

// SetMaxLimitRules lowers the number of limit rules a tenant may have and returns the function that
// restores it (T-675 creates rules under concurrency against a bound of a few).
func SetMaxLimitRules(n int) (restore func()) {
	old := maxLimitRules
	maxLimitRules = n
	return func() { maxLimitRules = old }
}
