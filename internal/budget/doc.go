// Package budget implements the budget gate and the budget actions.
//
// The gate meters every run in every mode. Unlimited, the default, applies
// no monetary limit of the factory's own and still records each run's usage;
// an alert threshold notifies the Owner without stopping anything; capped
// reserves before a run starts, suspends a run whose usage outruns its
// reservation, starts a new period on schedule and keeps a rollback reserve
// that only rollback runs may spend. Claims are refused until the Owner has
// confirmed the budget at install.
//
// Usage a provider could not confirm is reported as unverified, and a
// provider without a hard cap of its own is flagged: the factory's cap is a
// promise about what it admits, never about what a provider charges.
package budget
