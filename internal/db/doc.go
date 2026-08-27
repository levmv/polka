// Package db owns Polka's persisted catalog operations and the entity
// invariants that must hold for every caller. Higher-level workflows, pure
// format and storage policy, and filesystem or network work belong outside this
// package.
//
// SQL handle types make transaction ownership explicit. Read helpers accept
// Queryer so they work against either a database or an existing transaction.
// Mutations that consist of one atomic SQL statement accept Execer for the same
// flexibility. A mutation requiring several related statements accepts
// *sql.Tx, which means its caller must establish the transaction boundary,
// normally with DB.Transact. Methods on *DB are higher-level entry points and do
// not imply a transaction by themselves.
package db
