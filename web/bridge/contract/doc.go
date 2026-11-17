// Package contract is the fleet loop's wire contract (P5 decision 1): the types
// the bridge encodes and its Go clients decode, and the source of
// web/ui/src/lib/contract.ts. It holds types, the constants of their
// enumerations, and data; it declares no functions, and keeps no state.
// Standard library only.
//
// The one exception to "no methods" is a type that has to keep a journal
// written by an earlier binary byte-identical: FactoryApproval and
// FactoryWorker each carry a MarshalJSON that omits the fields that binary
// never wrote (factory.go). A Go method cannot be declared outside its type's
// package, so those two moved here with their types. They add no JSON key to
// the wire, and factory_tags_test.go pins every tag they write.
package contract
