// Package derivation executes versioned monotonic rules over canonical facts.
//
// The package deliberately contains no semantic rules. Callers register rules
// behind the small Rule port and receive derived facts with explicit lineage.
package derivation
