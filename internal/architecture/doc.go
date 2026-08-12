// Package architecture holds no code. It exists so that the shape of the
// module — which package may depend on which, and which concerns are allowed
// exactly one home — is checked by the test suite rather than remembered.
//
// Heikou's layering is currently correct: the package graph is acyclic, domain
// types sit in a leaf, and the terminal UI talks to a control.Service interface
// rather than a concrete controller. None of that is written down anywhere the
// compiler can read, so nothing stops a feature from adding the edge that makes
// it wrong. An import from internal/control back into internal/ui would compile
// and pass every existing test.
//
// The tests here fail on that edge instead, at the moment it is introduced,
// when moving it is still cheap.
package architecture
