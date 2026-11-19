package admin

// SetMaxLimitRules lowers the number of limit rules a tenant may have and returns the function that
// restores it (T-675 creates rules under concurrency against a bound of a few).
func SetMaxLimitRules(n int) (restore func()) {
	old := maxLimitRules
	maxLimitRules = n
	return func() { maxLimitRules = old }
}

// SetAfterRuleDeletion sets the seam that fails a transaction after the rules of a subject are
// deleted (T-714) and returns the function that clears it.
func SetAfterRuleDeletion(f func() error) (restore func()) {
	old := afterRuleDeletion
	afterRuleDeletion = f
	return func() { afterRuleDeletion = old }
}

// SetAfterTenantPriceInsert sets the seam that fails a transaction after a tenant price version and its
// rates are inserted (T-751) and returns the function that clears it.
func SetAfterTenantPriceInsert(f func() error) (restore func()) {
	old := afterTenantPriceInsert
	afterTenantPriceInsert = f
	return func() { afterTenantPriceInsert = old }
}

// SetAfterActiveCurrencySet sets the seam that fails a transaction after the active currency of the
// tenant is written (T-751) and returns the function that clears it.
func SetAfterActiveCurrencySet(f func() error) (restore func()) {
	old := afterActiveCurrencySet
	afterActiveCurrencySet = f
	return func() { afterActiveCurrencySet = old }
}

// SetMaxTenantPriceVersions lowers the number of price versions a tenant may have and returns the
// function that restores it (T-749).
func SetMaxTenantPriceVersions(n int) (restore func()) {
	old := maxTenantPriceVersions
	maxTenantPriceVersions = n
	return func() { maxTenantPriceVersions = old }
}
